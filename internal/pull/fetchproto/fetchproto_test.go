package fetchproto

import "testing"

func TestParseDockerConfig(t *testing.T) {
	modern := []byte(`{"auths":{"registry.example.com":{"username":"u","password":"p"}}}`)
	legacy := []byte(`{"registry.example.com":{"auth":"dTpw"}}`)

	for name, data := range map[string][]byte{"dockerconfigjson": modern, "dockercfg": legacy} {
		auths, err := ParseDockerConfig(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		a, ok := auths["registry.example.com"]
		if !ok {
			t.Fatalf("%s: entry missing: %v", name, auths)
		}
		if u, p, err := a.Credentials(); err != nil || u != "u" || p != "p" {
			t.Errorf("%s: credentials = (%q, %q, %v), want (u, p)", name, u, p, err)
		}
	}

	if _, err := ParseDockerConfig([]byte("not json")); err == nil {
		t.Error("expected an error for malformed config")
	}
}

func TestLookupNormalizesKeys(t *testing.T) {
	auths := map[string]Auth{
		"https://index.docker.io/v1/":   {Username: "hub"},
		"https://registry.example.com/": {Username: "ex"},
		"registry.local:5000":           {Username: "local"},
	}
	tests := map[string]string{
		"index.docker.io":      "hub",
		"docker.io":            "hub",
		"registry.example.com": "ex",
		"REGISTRY.EXAMPLE.COM": "ex",
		"registry.local:5000":  "local",
	}
	for host, want := range tests {
		a, ok := Lookup(auths, host)
		if !ok || a.Username != want {
			t.Errorf("Lookup(%q) = (%+v, %v), want user %q", host, a, ok, want)
		}
	}
	if _, ok := Lookup(auths, "registry.local"); ok {
		t.Error("a host must not match the same host on another port")
	}
}

func TestCredentialsRejectsMalformedAuth(t *testing.T) {
	if _, _, err := (Auth{Auth: "!!!"}).Credentials(); err == nil {
		t.Error("expected an error for invalid base64")
	}
	if _, _, err := (Auth{Auth: "bm9jb2xvbg=="}).Credentials(); err == nil {
		t.Error("expected an error for an auth field without a colon")
	}
}
