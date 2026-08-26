package completion_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/completion"
	"github.com/spf13/cobra"
)

func testRoot() *cobra.Command {
	root := &cobra.Command{Use: "kelper"}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(&cobra.Command{Use: "healthcheck", Short: "Check cluster health"})
	root.AddCommand(completion.Command())
	return root
}

func TestRunWritesScriptToStdout(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		var out bytes.Buffer
		if err := completion.Run(testRoot(), shell, "", &out); err != nil {
			t.Fatalf("%s: unexpected error: %v", shell, err)
		}
		if out.Len() == 0 {
			t.Errorf("%s: expected a non-empty script", shell)
		}
		// The script's whole job is to call back into `kelper __complete`.
		if !strings.Contains(out.String(), cobra.ShellCompRequestCmd) {
			t.Errorf("%s: script does not reference %s", shell, cobra.ShellCompRequestCmd)
		}
	}
}

func TestRunWritesScriptToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kelper.bash")
	var out bytes.Buffer
	if err := completion.Run(testRoot(), "bash", path, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected a non-empty script file")
	}
	// Stdout carries the confirmation, not the script itself.
	if strings.Contains(out.String(), "__kelper_get_completion_results") {
		t.Error("expected the script to go to the file, not stdout")
	}
	if !strings.Contains(out.String(), path) {
		t.Errorf("expected confirmation naming %s, got %q", path, out.String())
	}
}

func TestRunRejectsUnknownShell(t *testing.T) {
	var out bytes.Buffer
	err := completion.Run(testRoot(), "csh", "", &out)
	if err == nil {
		t.Fatal("expected an error for an unsupported shell")
	}
	if !strings.Contains(err.Error(), "csh") {
		t.Errorf("expected the error to name the shell, got: %v", err)
	}
}

func TestCommandRequiresExactlyOneShell(t *testing.T) {
	root := testRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"completion"})
	if err := root.Execute(); err == nil {
		t.Error("expected an error when no shell is given")
	}
}
