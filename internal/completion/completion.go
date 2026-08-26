// Package completion provides kelper's shell completion command.
//
// It replaces cobra's built-in completion command so that the generated script
// can be written straight to a file with --output, matching the ergonomics of
// `kelper kubeconfig --output`.
package completion

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// generator writes the completion script for one shell to w.
type generator func(root *cobra.Command, w io.Writer) error

var generators = map[string]generator{
	"bash": func(root *cobra.Command, w io.Writer) error {
		return root.GenBashCompletionV2(w, true)
	},
	"zsh": func(root *cobra.Command, w io.Writer) error {
		return root.GenZshCompletion(w)
	},
	"fish": func(root *cobra.Command, w io.Writer) error {
		return root.GenFishCompletion(w, true)
	},
	"powershell": func(root *cobra.Command, w io.Writer) error {
		return root.GenPowerShellCompletionWithDesc(w)
	},
}

// installHints tells the user what to do with the generated script.
var installHints = map[string]string{
	"bash": `  # current shell
  source <(kelper completion bash)

  # every new shell
  kelper completion bash --output ~/.local/share/bash-completion/completions/kelper

Requires the bash-completion package to be installed and sourced.`,
	"zsh": `  # current shell
  source <(kelper completion zsh)

  # every new shell (any directory on $fpath)
  kelper completion zsh --output "${fpath[1]}/_kelper"

Requires "autoload -U compinit && compinit" in ~/.zshrc.`,
	"fish": `  # current shell
  kelper completion fish | source

  # every new shell
  kelper completion fish --output ~/.config/fish/completions/kelper.fish`,
	"powershell": `  # current shell
  kelper completion powershell | Out-String | Invoke-Expression

  # every new shell: append the output to your PowerShell profile
  kelper completion powershell --output kelper.ps1`,
}

// Command returns the completion command tree. Cobra's default completion
// command must be disabled by the caller via
// root.CompletionOptions.DisableDefaultCmd.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion scripts",
		Long: "Generate a shell completion script for kelper.\n\n" +
			"The script must be sourced by your shell, not executed. Use --output to\n" +
			"write it directly to a file instead of stdout.",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
	}
	var outputFile string
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "write the script to a file (default: stdout)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return Run(cmd.Root(), args[0], outputFile, cmd.OutOrStdout())
	}
	return cmd
}

// Run generates the completion script for shell and writes it either to
// outputFile or, when that is empty, to stdout.
func Run(root *cobra.Command, shell, outputFile string, stdout io.Writer) error {
	gen, ok := generators[shell]
	if !ok {
		return fmt.Errorf("unsupported shell %q: expected bash, zsh, fish or powershell", shell)
	}

	var buf bytes.Buffer
	if err := gen(root, &buf); err != nil {
		return fmt.Errorf("generate %s completion: %w", shell, err)
	}

	if outputFile == "" {
		_, err := stdout.Write(buf.Bytes())
		return err
	}

	if err := os.WriteFile(outputFile, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write output file: %w", err)
	}
	fmt.Fprintf(stdout, "%s completion script written to %s\n\nTo load it:\n%s\n", shell, outputFile, installHints[shell])
	return nil
}
