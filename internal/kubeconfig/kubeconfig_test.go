package kubeconfig_test

import (
	"testing"

	"github.com/jthunderbird/kelper/internal/kubeconfig"
)

func TestValidateOptionsRequiresUser(t *testing.T) {
	opts := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeReadonly,
		Namespace:   "default",
	}
	if err := kubeconfig.ValidateOptions(opts); err == nil {
		t.Error("expected error when Username is empty")
	}
}

func TestValidateOptionsRequiresNamespaceForReadonly(t *testing.T) {
	opts := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeReadonly,
		Username:    "john",
	}
	if err := kubeconfig.ValidateOptions(opts); err == nil {
		t.Error("expected error when Namespace is empty for readonly account type")
	}
}

func TestValidateOptionsAllowsEmptyNamespaceForCluster(t *testing.T) {
	opts := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeCluster,
		Username:    "john",
	}
	if err := kubeconfig.ValidateOptions(opts); err != nil {
		t.Errorf("expected no error for cluster account type with no namespace, got: %v", err)
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

func TestGenerateRSAKeyReturnsPEMBytes(t *testing.T) {
	key, err := kubeconfig.GenerateRSAKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(key) == 0 {
		t.Error("expected non-empty key bytes")
	}
}
