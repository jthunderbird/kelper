package kubeconfig

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"
)

// writeSourceKubeconfig writes a kubeconfig with two clusters, so that a
// non-deterministic cluster pick would show up as a flaky test.
func writeSourceKubeconfig(t *testing.T) string {
	t.Helper()
	cfg := clientcmdapi.Config{
		APIVersion: "v1",
		Kind:       "Config",
		Clusters: map[string]*clientcmdapi.Cluster{
			"wanted": {Server: "https://wanted:6443", CertificateAuthorityData: []byte("wanted-ca")},
			"other":  {Server: "https://other:6443", CertificateAuthorityData: []byte("other-ca")},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"admin": {Token: "t"}},
		Contexts: map[string]*clientcmdapi.Context{
			"wanted": {Cluster: "wanted", AuthInfo: "admin"},
			"other":  {Cluster: "other", AuthInfo: "admin"},
		},
		CurrentContext: "wanted",
	}
	path := filepath.Join(t.TempDir(), "config")
	if err := clientcmd.WriteToFile(cfg, path); err != nil {
		t.Fatalf("write source kubeconfig: %v", err)
	}
	return path
}

// TestBuildKubeconfigIsLoadableByClientcmd guards the v1 on-disk schema:
// marshalling clientcmdapi.Config directly emits name-keyed maps, which fail
// to load with "cannot unmarshal object into Go struct field
// Config.clusters of type []v1.NamedCluster".
func TestBuildKubeconfigIsLoadableByClientcmd(t *testing.T) {
	opts := Options{
		AccountType:    AccountTypeReadonly,
		Username:       "bob",
		KubeconfigPath: writeSourceKubeconfig(t),
	}
	out, err := buildKubeconfig(opts, []byte("cert"), []byte("key"))
	if err != nil {
		t.Fatalf("buildKubeconfig: %v", err)
	}

	// Round-trip through the same loader kubectl uses.
	loaded, err := clientcmd.Load(out)
	if err != nil {
		t.Fatalf("generated kubeconfig is not loadable: %v", err)
	}
	if loaded.CurrentContext != "bob@wanted" {
		t.Errorf("expected current-context bob@wanted, got %q", loaded.CurrentContext)
	}
	if got := loaded.Clusters["wanted"].Server; got != "https://wanted:6443" {
		t.Errorf("expected the current context's cluster server, got %q", got)
	}

	// The on-disk shape must use lists, not maps.
	var raw struct {
		Clusters []map[string]any `json:"clusters"`
		Contexts []map[string]any `json:"contexts"`
		Users    []map[string]any `json:"users"`
	}
	if err := yaml.Unmarshal(out, &raw); err != nil {
		t.Fatalf("generated kubeconfig does not use v1 list shape: %v", err)
	}
	if len(raw.Clusters) != 1 || len(raw.Contexts) != 1 || len(raw.Users) != 1 {
		t.Errorf("expected one cluster, context and user, got %d/%d/%d",
			len(raw.Clusters), len(raw.Contexts), len(raw.Users))
	}
}

func TestBuildKubeconfigUsesCurrentContextCluster(t *testing.T) {
	path := writeSourceKubeconfig(t)
	// Repeat, since an arbitrary map pick would only fail intermittently.
	for i := 0; i < 20; i++ {
		out, err := buildKubeconfig(Options{Username: "bob", KubeconfigPath: path}, []byte("c"), []byte("k"))
		if err != nil {
			t.Fatalf("buildKubeconfig: %v", err)
		}
		loaded, err := clientcmd.Load(out)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if _, ok := loaded.Clusters["other"]; ok {
			t.Fatal("picked the cluster not referenced by the current context")
		}
	}
}

func TestBuildKubeconfigSetsNamespaceOnlyWhenScoped(t *testing.T) {
	path := writeSourceKubeconfig(t)

	scoped, err := buildKubeconfig(Options{Username: "bob", Namespace: "kyverno", KubeconfigPath: path}, []byte("c"), []byte("k"))
	if err != nil {
		t.Fatalf("buildKubeconfig: %v", err)
	}
	loaded, err := clientcmd.Load(scoped)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := loaded.Contexts["bob@wanted"].Namespace; got != "kyverno" {
		t.Errorf("expected context namespace kyverno, got %q", got)
	}

	clusterWide, err := buildKubeconfig(Options{Username: "bob", KubeconfigPath: path}, []byte("c"), []byte("k"))
	if err != nil {
		t.Fatalf("buildKubeconfig: %v", err)
	}
	loaded, err = clientcmd.Load(clusterWide)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := loaded.Contexts["bob@wanted"].Namespace; got != "" {
		t.Errorf("expected no context namespace for a cluster-wide account, got %q", got)
	}
}

// TestBuildKubeconfigInlinesCAFile covers source kubeconfigs that reference the
// CA by path rather than embedding it.
func TestBuildKubeconfigInlinesCAFile(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPath, []byte("file-ca"), 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	cfg := clientcmdapi.Config{
		APIVersion: "v1",
		Kind:       "Config",
		Clusters: map[string]*clientcmdapi.Cluster{
			"c": {Server: "https://c:6443", CertificateAuthority: caPath},
		},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"admin": {Token: "t"}},
		Contexts:       map[string]*clientcmdapi.Context{"c": {Cluster: "c", AuthInfo: "admin"}},
		CurrentContext: "c",
	}
	path := filepath.Join(dir, "config")
	if err := clientcmd.WriteToFile(cfg, path); err != nil {
		t.Fatalf("write source kubeconfig: %v", err)
	}

	out, err := buildKubeconfig(Options{Username: "bob", KubeconfigPath: path}, []byte("c"), []byte("k"))
	if err != nil {
		t.Fatalf("buildKubeconfig: %v", err)
	}
	loaded, err := clientcmd.Load(out)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := string(loaded.Clusters["c"].CertificateAuthorityData); got != "file-ca" {
		t.Errorf("expected the CA file to be inlined, got %q", got)
	}
}

func TestPolicyRule(t *testing.T) {
	ro := policyRule(Options{AccountType: AccountTypeReadonly})
	if len(ro.Verbs) != 3 || ro.Verbs[0] != "get" {
		t.Errorf("expected readonly verbs get,list,watch, got %v", ro.Verbs)
	}
	admin := policyRule(Options{AccountType: AccountTypeAdmin})
	if len(admin.Verbs) != 1 || admin.Verbs[0] != "*" {
		t.Errorf("expected admin verb *, got %v", admin.Verbs)
	}
	scoped := policyRule(Options{
		AccountType: AccountTypeScoped,
		APIGroups:   []string{""},
		Resources:   []string{"pods"},
		Verbs:       []string{"get"},
	})
	if len(scoped.Resources) != 1 || scoped.Resources[0] != "pods" {
		t.Errorf("expected scoped resources to be passed through, got %v", scoped.Resources)
	}
}
