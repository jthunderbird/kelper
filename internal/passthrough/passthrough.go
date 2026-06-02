package passthrough

import (
	"os"
	"os/exec"
)

// Run execs kubectl with the given args, inheriting stdin/stdout/stderr and
// the caller's exit code. This is a process replacement — it does not return
// on success; the process exits with kubectl's exit code.
func Run(args []string) error {
	return RunWith("kubectl", args)
}

// RunWith execs the named binary with args, inheriting stdin/stdout/stderr.
// Exposed for testing with an alternate binary path.
func RunWith(binary string, args []string) error {
	cmd := exec.Command(binary, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
