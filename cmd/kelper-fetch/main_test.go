package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/jthunderbird/kelper/internal/pull/fetchproto"
)

// pushImage starts an in-memory registry, pushes a random image to it and
// returns the image and its reference.
func pushImage(t *testing.T, handler func(http.Handler) http.Handler) (v1.Image, string) {
	t.Helper()
	var h http.Handler = registry.New()
	if handler != nil {
		h = handler(h)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	img, err := random.Image(1024, 3)
	if err != nil {
		t.Fatal(err)
	}
	ref := strings.TrimPrefix(srv.URL, "http://") + "/team/app:1.0"
	tag, err := name.NewTag(ref, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(tag, img, remote.WithAuth(&authn.Basic{Username: "puller", Password: "s3cret"})); err != nil {
		t.Fatal(err)
	}
	return img, ref
}

func TestFetchDockerArchive(t *testing.T) {
	img, ref := pushImage(t, nil)

	var buf bytes.Buffer
	req := fetchproto.Request{Ref: ref, Tag: ref, Platform: "linux/amd64", Format: fetchproto.FormatDocker, Insecure: true}
	if err := fetch(context.Background(), req, &buf); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	got, err := tarball.Image(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf.Bytes())), nil }, nil)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	assertSameImage(t, img, got)
}

func TestFetchOCIArchive(t *testing.T) {
	img, ref := pushImage(t, nil)

	var buf bytes.Buffer
	req := fetchproto.Request{Ref: ref, Tag: ref, Platform: "linux/amd64", Format: fetchproto.FormatOCI, Insecure: true}
	if err := fetch(context.Background(), req, &buf); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	files := map[string][]byte{}
	tr := tar.NewReader(&buf)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		files[h.Name] = data
	}

	// Every blob must be stored under its own digest.
	for path, data := range files {
		if hexDigest, ok := strings.CutPrefix(path, "blobs/sha256/"); ok {
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != hexDigest {
				t.Errorf("blob %s has wrong content", path)
			}
		}
	}

	var index struct {
		Manifests []struct {
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(files["index.json"], &index); err != nil {
		t.Fatalf("index.json: %v", err)
	}
	want, _ := img.Digest()
	if len(index.Manifests) != 1 || index.Manifests[0].Digest != want.String() {
		t.Fatalf("index manifests = %+v, want digest %s", index.Manifests, want)
	}
	for _, key := range []string{"org.opencontainers.image.ref.name", "io.containerd.image.name"} {
		if got := index.Manifests[0].Annotations[key]; got != ref {
			t.Errorf("annotation %s = %q, want %q", key, got, ref)
		}
	}
	layers, _ := img.Layers()
	for _, l := range layers {
		d, _ := l.Digest()
		if _, ok := files[blobPath(d)]; !ok {
			t.Errorf("layer %s missing from layout", d)
		}
	}
	if string(files["oci-layout"]) != `{"imageLayoutVersion":"1.0.0"}` {
		t.Errorf("oci-layout = %q", files["oci-layout"])
	}
}

// Credentials from the pod's pull secrets must reach the registry.
func TestFetchSendsPullSecretCredentials(t *testing.T) {
	requireAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, p, ok := r.BasicAuth(); !ok || u != "puller" || p != "s3cret" {
				w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	_, ref := pushImage(t, requireAuth)
	host := strings.SplitN(ref, "/", 2)[0]

	req := fetchproto.Request{Ref: ref, Tag: ref, Platform: "linux/amd64", Insecure: true}
	if err := fetch(context.Background(), req, io.Discard); err == nil {
		t.Fatal("expected anonymous fetch to be rejected")
	}

	req.Auths = map[string]fetchproto.Auth{host: {Username: "puller", Password: "s3cret"}}
	if err := fetch(context.Background(), req, io.Discard); err != nil {
		t.Fatalf("fetch with credentials: %v", err)
	}
}

func TestFetchSelectsPlatformFromIndex(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	amd, _ := random.Image(512, 1)
	arm, _ := random.Image(512, 1)
	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: amd, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
		mutate.IndexAddendum{Add: arm, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}},
	)
	ref := strings.TrimPrefix(srv.URL, "http://") + "/team/multi:1.0"
	tag, _ := name.NewTag(ref, name.Insecure)
	if err := remote.WriteIndex(tag, idx); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	req := fetchproto.Request{Ref: ref, Tag: ref, Platform: "linux/arm64", Insecure: true}
	if err := fetch(context.Background(), req, &buf); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got, err := tarball.Image(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf.Bytes())), nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertSameImage(t, arm, got)
}

func TestExplainAddsTLSHint(t *testing.T) {
	err := fmt.Errorf("Get https://r/v2/: tls: failed to verify certificate: x509: certificate signed by unknown authority")
	if !strings.Contains(explain(err), "--ca") {
		t.Errorf("explain() = %q, want a --ca hint", explain(err))
	}
	if strings.Contains(explain(errors.New("MANIFEST_UNKNOWN")), "--ca") {
		t.Error("explain() should not add a TLS hint to unrelated errors")
	}
}

func assertSameImage(t *testing.T, want, got v1.Image) {
	t.Helper()
	wantCfg, _ := want.ConfigName()
	gotCfg, err := got.ConfigName()
	if err != nil {
		t.Fatal(err)
	}
	if wantCfg != gotCfg {
		t.Errorf("config digest = %s, want %s", gotCfg, wantCfg)
	}
	wl, _ := want.Layers()
	gl, _ := got.Layers()
	if len(wl) != len(gl) {
		t.Fatalf("layers = %d, want %d", len(gl), len(wl))
	}
	for i := range wl {
		wd, _ := wl[i].Digest()
		gd, _ := gl[i].Digest()
		if wd != gd {
			t.Errorf("layer %d digest = %s, want %s", i, gd, wd)
		}
	}
}
