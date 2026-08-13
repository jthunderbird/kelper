package passthrough

import (
	"os"
	"os/exec"

	"github.com/jthunderbird/kelper/internal/failover"
)

// Run resolves the kubeconfig (honoring kelper's multi-server failover) and
// execs kubectl with the given args, inheriting stdin/stdout/stderr and the
// caller's exit code.
func Run(args []string) error {
	return RunWithKubeconfig("", args)
}

// RunWithKubeconfig resolves kubeconfigPath through failover (empty uses the
// default discovery rules) and execs kubectl. When a multi-server context
// requires failover, a temporary single-server kubeconfig is passed to kubectl
// via the KUBECONFIG environment variable.
func RunWithKubeconfig(kubeconfigPath string, args []string) error {
	resolved, cleanup, err := failover.Resolve(kubeconfigPath)
	if err != nil {
		return err
	}
	defer cleanup()

	cmd := exec.Command("kubectl", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if resolved != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+resolved)
	}
	return cmd.Run()
}

// RunWith execs the named binary with args, inheriting stdin/stdout/stderr.
// Exposed for testing with an alternate binary path. It does not apply failover
// resolution.
func RunWith(binary string, args []string) error {
	cmd := exec.Command(binary, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
