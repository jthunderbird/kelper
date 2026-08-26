package kubeconfig_test

import (
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/kubeconfig"
)

func TestValidateOptionsAllowsEmptyUsername(t *testing.T) {
	// An absent username is generated at run time, so validation must pass.
	opts := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeReadonly,
		Namespace:   "default",
	}
	if err := kubeconfig.ValidateOptions(opts); err != nil {
		t.Errorf("expected no error for empty Username, got: %v", err)
	}
}

func TestValidateOptionsRejectsInvalidUsername(t *testing.T) {
	opts := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeReadonly,
		Username:    "John Doe",
	}
	if err := kubeconfig.ValidateOptions(opts); err == nil {
		t.Error("expected error for username that is not a valid RFC 1123 subdomain")
	}
}

func TestValidateOptionsAllowsEmptyNamespaceForReadonly(t *testing.T) {
	// No namespace means cluster-wide, which is the default scope.
	opts := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeReadonly,
		Username:    "john",
	}
	if err := kubeconfig.ValidateOptions(opts); err != nil {
		t.Errorf("expected no error for readonly with no namespace, got: %v", err)
	}
}

func TestValidateOptionsAllowsEmptyNamespaceForAdmin(t *testing.T) {
	opts := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeAdmin,
		Username:    "john",
	}
	if err := kubeconfig.ValidateOptions(opts); err != nil {
		t.Errorf("expected no error for admin with no namespace, got: %v", err)
	}
}

func TestValidateOptionsScopedRequiresResources(t *testing.T) {
	opts := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeScoped,
		Username:    "john",
		Namespace:   "default",
	}
	if err := kubeconfig.ValidateOptions(opts); err == nil {
		t.Error("expected error when Resources is empty for scoped account type")
	}
}

func TestValidateOptionsScopedAllowsEmptyNamespace(t *testing.T) {
	opts := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeScoped,
		Username:    "john",
		Resources:   []string{"pods"},
		APIGroups:   []string{"*"},
		Verbs:       []string{"get"},
	}
	if err := kubeconfig.ValidateOptions(opts); err != nil {
		t.Errorf("expected no error for scoped with no namespace, got: %v", err)
	}
}

func TestClusterWide(t *testing.T) {
	if !(kubeconfig.Options{}).ClusterWide() {
		t.Error("expected empty namespace to mean cluster-wide")
	}
	if (kubeconfig.Options{Namespace: "kyverno"}).ClusterWide() {
		t.Error("expected a set namespace to mean namespace-scoped")
	}
}

func TestValidateUsername(t *testing.T) {
	valid := []string{"john", "john-doe", "readonly-3f9a1c07", "a", "a1"}
	for _, name := range valid {
		if err := kubeconfig.ValidateUsername(name); err != nil {
			t.Errorf("expected %q to be valid, got: %v", name, err)
		}
	}
	invalid := []string{"", "John", "john doe", "john@example.com", "-john", "john-", "john_doe", strings.Repeat("a", 254)}
	for _, name := range invalid {
		if err := kubeconfig.ValidateUsername(name); err == nil {
			t.Errorf("expected %q to be invalid", name)
		}
	}
}

func TestGenerateUsernameFormat(t *testing.T) {
	name, err := kubeconfig.GenerateUsername(kubeconfig.AccountTypeReadonly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(name, "readonly-") {
		t.Errorf("expected generated name to be prefixed with the account type, got %q", name)
	}
	if got, want := len(name), len("readonly-")+8; got != want {
		t.Errorf("expected an 8-character hex suffix (len %d), got %q (len %d)", want, name, got)
	}
	if err := kubeconfig.ValidateUsername(name); err != nil {
		t.Errorf("generated username %q is not a valid object name: %v", name, err)
	}
}

func TestGenerateUsernameIsUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		name, err := kubeconfig.GenerateUsername(kubeconfig.AccountTypeAdmin)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if seen[name] {
			t.Fatalf("duplicate generated username %q", name)
		}
		seen[name] = true
	}
}

func TestGenerateRSAKeyReturnsPEMBytes(t *testing.T) {
	key, err := kubeconfig.GenerateRSAKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(key) == 0 {
		t.Error("expected non-empty key bytes")
	}
}

func TestWizardModelInitialStep(t *testing.T) {
	m := kubeconfig.NewWizardModel()
	if m.Step() != 0 {
		t.Errorf("expected initial step 0, got %d", m.Step())
	}
}

func TestParseAPIGroups(t *testing.T) {
	// An unset or blank flag means every API group.
	for _, in := range []string{"", "   "} {
		got := kubeconfig.ParseAPIGroups(in)
		if len(got) != 1 || got[0] != "*" {
			t.Errorf("ParseAPIGroups(%q) = %v, want [*]", in, got)
		}
	}
	// "core" is the only way to spell the empty-string core API group, since
	// splitting a CSV list discards empty entries.
	got := kubeconfig.ParseAPIGroups("core")
	if len(got) != 1 || got[0] != "" {
		t.Errorf(`ParseAPIGroups("core") = %v, want [""]`, got)
	}
	got = kubeconfig.ParseAPIGroups("core,apps")
	if len(got) != 2 || got[0] != "" || got[1] != "apps" {
		t.Errorf(`ParseAPIGroups("core,apps") = %v, want ["" apps]`, got)
	}
}

func TestValidateOptionsScopedRejectsEmptyVerbsAndGroups(t *testing.T) {
	base := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeScoped,
		Username:    "john",
		Resources:   []string{"pods"},
		APIGroups:   []string{"*"},
		Verbs:       []string{"get"},
	}
	if err := kubeconfig.ValidateOptions(base); err != nil {
		t.Fatalf("expected the base scoped options to be valid, got: %v", err)
	}

	noVerbs := base
	noVerbs.Verbs = nil
	if err := kubeconfig.ValidateOptions(noVerbs); err == nil {
		t.Error("expected an error when scoped verbs are empty")
	}

	noGroups := base
	noGroups.APIGroups = nil
	if err := kubeconfig.ValidateOptions(noGroups); err == nil {
		t.Error("expected an error when scoped API groups are empty")
	}
}
