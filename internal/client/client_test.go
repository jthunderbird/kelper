package client_test

import (
	"testing"

	"github.com/jthunderbird/kelper/internal/client"
)

func TestNewClientReturnsErrorWithBadKubeconfig(t *testing.T) {
	_, err := client.New("/nonexistent/path/to/kubeconfig")
	if err == nil {
		t.Fatal("expected error for nonexistent kubeconfig, got nil")
	}
}
