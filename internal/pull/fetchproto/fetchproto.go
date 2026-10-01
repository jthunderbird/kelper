// Package fetchproto is the contract between kelper and kelper-fetch, the
// helper binary kelper injects into a pod to pull an image from inside the
// cluster. It also parses and matches registry credentials.
//
// It depends only on the standard library so that it adds nothing to either
// binary beyond what each already links.
package fetchproto

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Format names an archive layout the fetcher can write.
type Format string

const (
	// FormatDocker is a `docker load`-able tarball.
	FormatDocker Format = "docker"
	// FormatOCI is an OCI image layout, tarred.
	FormatOCI Format = "oci"
)

// Request is everything the fetcher needs. kelper sends it as one JSON object
// on the fetcher's stdin, which keeps credentials off the command line and off
// the container's disk.
type Request struct {
	// Ref is the image to pull, normally pinned as repo@sha256:...
	Ref string `json:"ref"`
	// Tag names the image inside a docker archive, e.g. quay.io/org/app:1.0.
	Tag string `json:"tag"`
	// Platform selects an image from an index, e.g. linux/amd64.
	Platform string `json:"platform"`
	Format   Format `json:"format"`
	// Auths maps a registry host to its credentials.
	Auths map[string]Auth `json:"auths,omitempty"`
	// CA is extra PEM-encoded certificate authorities to trust.
	CA string `json:"ca,omitempty"`
	// Insecure skips TLS verification and allows plain HTTP.
	Insecure bool `json:"insecure,omitempty"`
}

// Auth is one registry's credentials, in the shape of a docker config entry.
type Auth struct {
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	Auth          string `json:"auth,omitempty"`
	IdentityToken string `json:"identitytoken,omitempty"`
	RegistryToken string `json:"registrytoken,omitempty"`
}

// ParseDockerConfig reads the auth entries from a pull secret's payload. It
// accepts both the kubernetes.io/dockerconfigjson form ({"auths": {...}}) and
// the legacy kubernetes.io/dockercfg form, which is the bare auths map.
func ParseDockerConfig(data []byte) (map[string]Auth, error) {
	var cfg struct {
		Auths map[string]Auth `json:"auths"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse docker config: %w", err)
	}
	if cfg.Auths != nil {
		return cfg.Auths, nil
	}
	var legacy map[string]Auth
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("parse docker config: %w", err)
	}
	return legacy, nil
}

// Lookup returns the credentials for a registry host. Config keys are matched
// the way the docker CLI writes them: a bare host, a host with a scheme or
// path, or one of Docker Hub's several names.
func Lookup(auths map[string]Auth, registry string) (Auth, bool) {
	want := normalizeHost(registry)
	for key, a := range auths {
		if normalizeHost(key) == want {
			return a, true
		}
	}
	return Auth{}, false
}

// Credentials resolves an entry to a username and password, decoding the
// combined base64 "auth" field when the separate fields are absent.
func (a Auth) Credentials() (username, password string, err error) {
	if a.Username != "" || a.Password != "" || a.Auth == "" {
		return a.Username, a.Password, nil
	}
	raw, err := base64.StdEncoding.DecodeString(a.Auth)
	if err != nil {
		return "", "", fmt.Errorf("decode auth field: %w", err)
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		return "", "", fmt.Errorf("auth field is not user:password")
	}
	return user, pass, nil
}

// dockerHubHosts are the names Docker Hub appears under in configs and refs.
var dockerHubHosts = map[string]bool{
	"docker.io":            true,
	"index.docker.io":      true,
	"registry-1.docker.io": true,
}

func normalizeHost(s string) string {
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(s)
	if dockerHubHosts[s] {
		return "docker.io"
	}
	return s
}
