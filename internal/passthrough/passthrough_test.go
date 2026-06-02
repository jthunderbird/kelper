package passthrough_test

import (
	"testing"

	"github.com/jthunderbird/kelper/internal/passthrough"
)

func TestRunReturnsErrorWhenKubectlMissing(t *testing.T) {
	// Override kubectl binary to something that doesn't exist.
	err := passthrough.RunWith("/nonexistent/binary", []string{"get", "pods"})
	if err == nil {
		t.Fatal("expected error when binary does not exist, got nil")
	}
}
