// Package completion provides kelper's shell completion command.
//
// It replaces cobra's built-in completion command to add --output, to expose
// each shell as a real subcommand so it shows up under "Available Commands",
// and to register completion for alias binaries such as `k`.
package completion

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// shell describes one supported shell: how to render its completion script and
// how a user wires that script into their startup file.
type shell struct {
	name  string
	short string
	gen   func(root *cobra.Command, w io.Writer) error
	// startupFile is the shell's rc file, as the user would type it.
	startupFile string
	// sourceLine renders the line appended to startupFile to load path.
	sourceLine func(path string) string
	// reload renders the command that loads startupFile into the current shell.
	reload string
}

var shells = []shell{
	{
		name:  "bash",
		short: "Generate the bash completion script",
		gen: func(root *cobra.Command, w io.Writer) error {
			return root.GenBashCompletionV2(w, true)
		},
		startupFile: "~/.bashrc",
		sourceLine:  func(path string) string { return "source " + path },
		reload:      "source ~/.bashrc",
	},
	{
		name:  "zsh",
		short: "Generate the zsh completion script",
		gen: func(root *cobra.Command, w io.Writer) error {
			return root.GenZshCompletion(w)
		},
		startupFile: "~/.zshrc",
		sourceLine:  func(path string) string { return "source " + path },
		reload:      "source ~/.zshrc",
	},
	{
		name:  "fish",
		short: "Generate the fish completion script",
		gen: func(root *cobra.Command, w io.Writer) error {
			return root.GenFishCompletion(w, true)
		},
		startupFile: "~/.config/fish/config.fish",
		sourceLine:  func(path string) string { return "source " + path },
		reload:      "source ~/.config/fish/config.fish",
	},
	{
		name:  "powershell",
		short: "Generate the powershell completion script",
		gen: func(root *cobra.Command, w io.Writer) error {
			return root.GenPowerShellCompletionWithDesc(w)
		},
		startupFile: "$PROFILE",
		sourceLine:  func(path string) string { return ". '" + path + "'" },
		reload:      ". $PROFILE",
	},
}

// DefaultAliases is completed alongside kelper itself. `k` is the near
// universal short name for a kubectl-style binary, and kelper is commonly
// symlinked or copied to it.
const DefaultAliases = "k"

// aliasPattern matches names that are safe to embed in the shell function names
// cobra derives from the binary name.
var aliasPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// Command returns the completion command tree. Cobra's default completion
// command must be disabled by the caller via
// root.CompletionOptions.DisableDefaultCmd.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion",
		Short: "Generate shell completion scripts",
		Long: "Generate a shell completion script for kelper.\n\n" +
			"The script must be sourced by your shell, not executed. Pass --output to\n" +
			"write it to a file and print the two commands that install it.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return fmt.Errorf("unknown shell %q\n\nSupported shells: %s\nRun \"%s --help\" for details",
				args[0], strings.Join(shellNames(), ", "), cmd.CommandPath())
		},
	}
	for _, sh := range shells {
		cmd.AddCommand(shellCommand(sh))
	}
	return cmd
}

func shellCommand(sh shell) *cobra.Command {
	var outputFile, aliases string
	cmd := &cobra.Command{
		Use:   sh.name,
		Short: sh.short,
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "write the script to a file (default: stdout)")
	cmd.Flags().StringVar(&aliases, "alias", DefaultAliases,
		`comma-separated alias binaries to also complete (--alias "" to disable)`)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return Run(cmd.Root(), sh.name, outputFile, splitCSV(aliases), cmd.OutOrStdout())
	}
	return cmd
}

func shellNames() []string {
	names := make([]string, len(shells))
	for i, sh := range shells {
		names[i] = sh.name
	}
	return names
}

func lookupShell(name string) (shell, bool) {
	for _, sh := range shells {
		if sh.name == name {
			return sh, true
		}
	}
	return shell{}, false
}

// Run generates the completion script for shellName and writes it either to
// outputFile or, when that is empty, to stdout. aliases names additional
// binaries (typically "k") that should be completed the same way.
func Run(root *cobra.Command, shellName, outputFile string, aliases []string, stdout io.Writer) error {
	sh, ok := lookupShell(shellName)
	if !ok {
		return fmt.Errorf("unsupported shell %q: expected %s", shellName, strings.Join(shellNames(), ", "))
	}
	for _, alias := range aliases {
		if !aliasPattern.MatchString(alias) {
			return fmt.Errorf("invalid alias %q: must start with a letter or underscore and contain only letters, digits, '-' and '_'", alias)
		}
	}

	var buf bytes.Buffer
	if err := sh.gen(root, &buf); err != nil {
		return fmt.Errorf("generate %s completion: %w", shellName, err)
	}
	// Every identifier in a generated script derives from the root command's
	// name, so re-rendering under an alias produces a second, self-contained
	// script that does not collide with the first.
	for _, alias := range aliases {
		if alias == root.Name() {
			continue
		}
		buf.WriteString("\n")
		if err := generateAs(root, alias, sh, &buf); err != nil {
			return fmt.Errorf("generate %s completion for alias %q: %w", shellName, alias, err)
		}
	}

	if outputFile == "" {
		_, err := stdout.Write(buf.Bytes())
		return err
	}

	if err := os.WriteFile(outputFile, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write output file: %w", err)
	}
	// The install commands are meant to be copy-pasted, so they must name the
	// script by an absolute path rather than whatever relative path was passed.
	absPath, err := filepath.Abs(outputFile)
	if err != nil {
		absPath = outputFile
	}
	printInstructions(stdout, sh, absPath, aliases)
	return nil
}

// generateAs renders sh's completion script as though the root command were
// named alias.
func generateAs(root *cobra.Command, alias string, sh shell, w io.Writer) error {
	originalUse := root.Use
	// Cobra takes the command name from the first word of Use; keep any
	// trailing usage text intact.
	root.Use = alias + strings.TrimPrefix(originalUse, root.Name())
	defer func() { root.Use = originalUse }()
	return sh.gen(root, w)
}

func printInstructions(w io.Writer, sh shell, absPath string, aliases []string) {
	fmt.Fprintf(w, "Wrote %s completion to %s\n\n", sh.name, absPath)
	fmt.Fprintf(w, "Run these two commands:\n\n")
	fmt.Fprintf(w, "  echo %q >> %s\n", sh.sourceLine(absPath), sh.startupFile)
	fmt.Fprintf(w, "  %s\n\n", sh.reload)
	fmt.Fprintf(w, "The first adds the script to %s so every new shell loads it.\n", sh.startupFile)
	fmt.Fprintf(w, "The second loads it into the shell you are in right now.\n")
	if len(aliases) > 0 {
		fmt.Fprintf(w, "\nAlso completes: %s\n", strings.Join(aliases, ", "))
	}
}

// splitCSV splits a comma-separated list, dropping empty entries so that
// --alias "" disables aliases entirely.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
