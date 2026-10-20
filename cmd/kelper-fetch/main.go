// Command kelper-fetch pulls one image from a registry and writes it to stdout
// as a docker or OCI archive.
//
// It is not meant to be run by hand. kelper injects it into a container inside
// the target pod, so the pull uses the pod's network path to the registry, and
// sends a fetchproto.Request on stdin. It depends on nothing in the container
// image: it is static and carries its own root certificates.
package main

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/jthunderbird/kelper/internal/pull/fetchproto"

	// Container images frequently ship no CA bundle (busybox, distroless), and
	// Go reads roots from the container's filesystem. The embedded Mozilla set
	// is used only when the system offers none.
	_ "golang.org/x/crypto/x509roots/fallback"
)

func main() {
	// The binary is only ever run once. Removing it straight away means a
	// failed or abandoned pull leaves nothing behind in the container.
	_ = os.Remove(os.Args[0])

	var req fetchproto.Request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		fail(fmt.Errorf("read request: %w", err))
	}

	out := bufio.NewWriterSize(os.Stdout, 1<<20)
	if err := fetch(context.Background(), req, out); err != nil {
		fail(err)
	}
	if err := out.Flush(); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "kelper-fetch: %s\n", explain(err))
	os.Exit(1)
}

// explain adds guidance to the errors a user can act on.
func explain(err error) string {
	msg := err.Error()
	var unknownCA x509.UnknownAuthorityError
	if errors.As(err, &unknownCA) || strings.Contains(msg, "x509:") {
		msg += " (the registry's certificate is not trusted: pass --ca <file> or --insecure-skip-tls-verify)"
	}
	return msg
}

// fetch resolves req.Ref for the requested platform and writes the archive.
func fetch(ctx context.Context, req fetchproto.Request, w io.Writer) error {
	var opts []name.Option
	if req.Insecure {
		opts = append(opts, name.Insecure)
	}
	ref, err := name.ParseReference(req.Ref, opts...)
	if err != nil {
		return fmt.Errorf("parse reference %q: %w", req.Ref, err)
	}
	platform, err := v1.ParsePlatform(req.Platform)
	if err != nil {
		return fmt.Errorf("parse platform %q: %w", req.Platform, err)
	}
	transport, err := newTransport(req)
	if err != nil {
		return err
	}

	img, err := remote.Image(ref,
		remote.WithContext(ctx),
		remote.WithPlatform(*platform),
		remote.WithAuthFromKeychain(keychain{req.Auths}),
		remote.WithTransport(transport),
	)
	if err != nil {
		return err
	}

	switch req.Format {
	case fetchproto.FormatOCI:
		return writeOCI(w, img, req.Tag)
	case fetchproto.FormatDocker, "":
		tag, err := name.NewTag(req.Tag, opts...)
		if err != nil {
			return fmt.Errorf("parse tag %q: %w", req.Tag, err)
		}
		return tarball.Write(tag, img, w)
	default:
		return fmt.Errorf("unknown format %q", req.Format)
	}
}

// newTransport applies the request's trust settings to the default transport.
func newTransport(req fetchproto.Request) (http.RoundTripper, error) {
	t := remote.DefaultTransport.(*http.Transport).Clone()
	cfg := &tls.Config{InsecureSkipVerify: req.Insecure} //nolint:gosec // explicit user opt-in
	if req.CA != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(req.CA)) {
			return nil, fmt.Errorf("--ca contains no PEM certificates")
		}
		cfg.RootCAs = pool
	}
	t.TLSClientConfig = cfg
	return t, nil
}

// keychain serves credentials collected from the pod's pull secrets, falling
// back to anonymous access for registries with no entry.
type keychain struct {
	auths map[string]fetchproto.Auth
}

func (k keychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	a, ok := fetchproto.Lookup(k.auths, target.RegistryStr())
	if !ok {
		return authn.Anonymous, nil
	}
	if a.IdentityToken != "" || a.RegistryToken != "" {
		return authn.FromConfig(authn.AuthConfig{
			IdentityToken: a.IdentityToken,
			RegistryToken: a.RegistryToken,
		}), nil
	}
	user, pass, err := a.Credentials()
	if err != nil {
		return nil, fmt.Errorf("credentials for %s: %w", target.RegistryStr(), err)
	}
	return &authn.Basic{Username: user, Password: pass}, nil
}

// writeOCI streams img as a tarred OCI image layout. go-containerregistry only
// writes layouts to a directory, which would need the whole image on the
// container's disk; every blob size is known from the manifest, so the tar can
// be produced as the blobs arrive instead.
func writeOCI(w io.Writer, img v1.Image, tag string) error {
	tw := tar.NewWriter(w)
	now := time.Now()
	writeFile := func(path string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: path, Mode: 0o644, Size: int64(len(data)), ModTime: now}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}

	if err := writeFile("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)); err != nil {
		return err
	}

	manifest, err := img.RawManifest()
	if err != nil {
		return err
	}
	digest, err := img.Digest()
	if err != nil {
		return err
	}
	mediaType, err := img.MediaType()
	if err != nil {
		return err
	}
	if err := writeFile(blobPath(digest), manifest); err != nil {
		return err
	}

	config, err := img.RawConfigFile()
	if err != nil {
		return err
	}
	configName, err := img.ConfigName()
	if err != nil {
		return err
	}
	if err := writeFile(blobPath(configName), config); err != nil {
		return err
	}

	layers, err := img.Layers()
	if err != nil {
		return err
	}
	written := map[v1.Hash]bool{}
	for _, l := range layers {
		d, err := l.Digest()
		if err != nil {
			return err
		}
		if written[d] {
			continue
		}
		size, err := l.Size()
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: blobPath(d), Mode: 0o644, Size: size, ModTime: now}); err != nil {
			return err
		}
		rc, err := l.Compressed()
		if err != nil {
			return err
		}
		// The remote reader verifies the digest at EOF, so a corrupt blob
		// fails here rather than producing a bad archive.
		_, err = io.Copy(tw, rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("layer %s: %w", d, err)
		}
		written[d] = true
	}

	index := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{{
			"mediaType": mediaType,
			"digest":    digest.String(),
			"size":      len(manifest),
			"annotations": map[string]string{
				// podman, skopeo and docker read the full image name from
				// ref.name, as podman writes it; containerd reads its own key.
				"org.opencontainers.image.ref.name": tag,
				"io.containerd.image.name":          tag,
			},
		}},
	}
	indexJSON, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if err := writeFile("index.json", indexJSON); err != nil {
		return err
	}
	return tw.Close()
}

func blobPath(h v1.Hash) string { return "blobs/" + h.Algorithm + "/" + h.Hex }
