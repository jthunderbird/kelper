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

var allShells = []string{"bash", "zsh", "fish", "powershell"}

func TestRunWritesScriptToStdout(t *testing.T) {
	for _, shell := range allShells {
		var out bytes.Buffer
		if err := completion.Run(testRoot(), shell, "", nil, &out); err != nil {
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

// Each shell is a real subcommand so that it appears under "Available
// Commands" in the help output rather than only in the usage line.
func TestEachShellIsASubcommand(t *testing.T) {
	cmd, _, err := testRoot().Find([]string{"completion"})
	if err != nil {
		t.Fatalf("find completion: %v", err)
	}
	got := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		got[sub.Name()] = true
		if sub.Short == "" {
			t.Errorf("%s: expected a Short description for the help listing", sub.Name())
		}
	}
	for _, shell := range allShells {
		if !got[shell] {
			t.Errorf("expected %q to be a subcommand of completion", shell)
		}
	}
}

func TestUnknownShellErrorNamesSupportedShells(t *testing.T) {
	root := testRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"completion", "tcsh"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error for an unsupported shell")
	}
	for _, want := range append([]string{"tcsh"}, allShells...) {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to mention %q, got: %v", want, err)
		}
	}
}

// Running `kelper completion` bare should show help, not fail.
func TestBareCompletionShowsHelp(t *testing.T) {
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"completion"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "Available Commands") {
		t.Errorf("expected help output listing the shells, got:\n%s", out.String())
	}
}

func TestRunWritesScriptToFileAndPrintsInstallCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kelper.bash")
	var out bytes.Buffer
	if err := completion.Run(testRoot(), "bash", path, nil, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected a non-empty script file")
	}
	// Stdout carries the install instructions, not the script itself.
	if strings.Contains(out.String(), "__kelper_get_completion_results") {
		t.Error("expected the script to go to the file, not stdout")
	}
	// The two commands must be copy-pasteable as-is.
	wantLines := []string{
		`echo "source ` + path + `" >> ~/.bashrc`,
		"source ~/.bashrc",
	}
	for _, want := range wantLines {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected instructions to contain %q, got:\n%s", want, out.String())
		}
	}
}

// The install commands must name the script by absolute path, since the user
// pastes them from a different working directory later.
func TestRunInstructionsUseAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var out bytes.Buffer
	if err := completion.Run(testRoot(), "bash", "kelper.bash", nil, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	abs := filepath.Join(dir, "kelper.bash")
	if !strings.Contains(out.String(), abs) {
		t.Errorf("expected the absolute path %q in the instructions, got:\n%s", abs, out.String())
	}
}

// Aliases let a kelper binary symlinked or copied to `k` complete too.
func TestRunRegistersAliases(t *testing.T) {
	tests := []struct {
		shell string
		want  string
	}{
		{"bash", "-F __start_k k"},
		{"zsh", "compdef _k k"},
		{"fish", "complete -c k "},
		{"powershell", "-CommandName 'k'"},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		if err := completion.Run(testRoot(), tt.shell, "", []string{"k"}, &out); err != nil {
			t.Fatalf("%s: unexpected error: %v", tt.shell, err)
		}
		if !strings.Contains(out.String(), tt.want) {
			t.Errorf("%s: expected the alias registration %q", tt.shell, tt.want)
		}
		// The primary name must still be registered alongside the alias.
		if !strings.Contains(out.String(), "kelper") {
			t.Errorf("%s: expected kelper itself to stay registered", tt.shell)
		}
	}
}

// Generating under an alias must not leave the command tree renamed.
func TestRunRestoresRootName(t *testing.T) {
	root := testRoot()
	var out bytes.Buffer
	if err := completion.Run(root, "bash", "", []string{"k"}, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root.Name() != "kelper" {
		t.Errorf("expected the root command to still be named kelper, got %q", root.Name())
	}
}

func TestRunSkipsAliasMatchingPrimaryName(t *testing.T) {
	var out bytes.Buffer
	if err := completion.Run(testRoot(), "bash", "", []string{"kelper"}, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.Count(out.String(), "__start_kelper()"); got != 1 {
		t.Errorf("expected kelper to be registered once, got %d registrations", got)
	}
}

func TestRunRejectsInvalidAlias(t *testing.T) {
	for _, alias := range []string{"1k", "k;rm -rf /", "k k", "-k", ""} {
		var out bytes.Buffer
		if err := completion.Run(testRoot(), "bash", "", []string{alias}, &out); err == nil {
			t.Errorf("expected alias %q to be rejected", alias)
		}
	}
}

func TestNoAliasesProducesSingleRegistration(t *testing.T) {
	var out bytes.Buffer
	if err := completion.Run(testRoot(), "bash", "", nil, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out.String(), "__start_k()") {
		t.Error("expected no alias registration when none are requested")
	}
}

// The --alias flag defaults to k and accepts a comma-separated list; an empty
// value disables aliases entirely.
func TestAliasFlag(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantK     bool
		wantKc    bool
		wantSlash bool
	}{
		{"default", []string{"completion", "bash"}, true, false, false},
		{"explicit list", []string{"completion", "bash", "--alias", "k,kc"}, true, true, false},
		{"disabled", []string{"completion", "bash", "--alias", ""}, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := testRoot()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(tt.args)
			if err := root.Execute(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := strings.Contains(out.String(), "__start_k()"); got != tt.wantK {
				t.Errorf("alias k registered = %v, want %v", got, tt.wantK)
			}
			if got := strings.Contains(out.String(), "__start_kc()"); got != tt.wantKc {
				t.Errorf("alias kc registered = %v, want %v", got, tt.wantKc)
			}
		})
	}
}

func TestDefaultAliasesIsK(t *testing.T) {
	if completion.DefaultAliases != "k" {
		t.Errorf("expected the default alias to be k, got %q", completion.DefaultAliases)
	}
}

func TestRunRejectsUnknownShell(t *testing.T) {
	var out bytes.Buffer
	err := completion.Run(testRoot(), "csh", "", nil, &out)
	if err == nil {
		t.Fatal("expected an error for an unsupported shell")
	}
	if !strings.Contains(err.Error(), "csh") {
		t.Errorf("expected the error to name the shell, got: %v", err)
	}
}

func TestShellSubcommandsRejectExtraArgs(t *testing.T) {
	root := testRoot()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"completion", "bash", "zsh"})
	if err := root.Execute(); err == nil {
		t.Error("expected an error for an unexpected positional argument")
	}
}
