package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/jthunderbird/kelper/internal/client"
	"github.com/jthunderbird/kelper/internal/completion"
	"github.com/jthunderbird/kelper/internal/get"
	"github.com/jthunderbird/kelper/internal/healthcheck"
	"github.com/jthunderbird/kelper/internal/images"
	"github.com/jthunderbird/kelper/internal/kubeconfig"
	"github.com/jthunderbird/kelper/internal/output"
	"github.com/jthunderbird/kelper/internal/passthrough"
	"github.com/jthunderbird/kelper/internal/resources"
	"github.com/jthunderbird/kelper/internal/volumes"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

// appVersion is overridden at build time via -ldflags "-X main.appVersion=...".
var appVersion = "dev"

// kelperSubcommands is the set of first-level subcommands that kelper handles
// natively. Everything else is transparently forwarded to kubectl.
var kelperSubcommands = map[string]bool{
	"healthcheck": true,
	"health":      true,
	"images":      true,
	"image":       true,
	"imgs":        true,
	"img":         true,
	"resources":   true,
	"resource":    true,
	"res":         true,
	"volumes":     true,
	"volume":      true,
	"vols":        true,
	"vol":         true,
	"kubeconfig":  true,
	"completion":  true,
	"help":        true,
	// Cobra's hidden completion callbacks. The generated shell scripts invoke
	// these on every keystroke; without them the fast-path below would forward
	// the request to kubectl and completion would silently do nothing.
	"__complete":       true,
	"__completeNoDesc": true,
}

func main() {
	var kubeconfigPath string
	var cs *kubernetes.Clientset

	// Fast-path: if the first non-flag argument is not a kelper-native
	// subcommand (and not a help/version flag), forward everything directly to
	// kubectl without involving cobra's subcommand routing.
	args := os.Args[1:]
	if len(args) > 0 {
		// Handle version flags up front.
		if args[0] == "-v" || args[0] == "--version" {
			fmt.Printf("kelper %s\n", appVersion)
			return
		}
		// kelper's global flags may precede the subcommand, so the routing
		// decision is made on the first non-flag argument rather than args[0].
		first, cmdIndex := firstCommand(args)
		isHelpFlag := args[0] == "--help" || args[0] == "-h"
		if !isHelpFlag && first != "" && !kelperSubcommands[first] {
			// kubectl rejects flags placed before its subcommand, so kelper's
			// own global flags are consumed here and re-applied out of band.
			globalPath := extractKubeconfigFlag(args[:cmdIndex])
			forwarded := args[cmdIndex:]
			// Intercept: get -o yaml (and not --raw)
			if first == "get" && isYAMLOutput(forwarded) && !hasFlag(forwarded, "--raw") {
				// Client init needed for get interception.
				var err error
				cs, err = client.New(globalPath)
				if err != nil {
					output.Errorf(os.Stderr, "could not connect to cluster: %s", err)
					os.Exit(1)
				}
				if err := get.Run(cs, forwarded); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
				return
			}
			// Everything else: transparent passthrough to kubectl.
			if err := passthrough.RunWithKubeconfig(globalPath, forwarded); err != nil {
				os.Exit(1)
			}
			return
		}
	}

	root := &cobra.Command{
		Use:           "kelper",
		Short:         "kubectl wrapper with enhanced output and interactivity",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// If no args, print help.
			if len(args) == 0 {
				return cmd.Help()
			}
			return nil
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip client init for completion and help — these don't need a
			// cluster. cobra.ShellCompRequestCmd is the hidden __complete
			// command the generated scripts call.
			switch cmd.Name() {
			case "completion", "help", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
				return nil
			}
			// Skip if any ancestor is completion (e.g. `kelper completion bash`).
			for c := cmd.Parent(); c != nil; c = c.Parent() {
				if c.Name() == "completion" {
					return nil
				}
			}
			// kubeconfig builds its own client internally.
			if cmd.Name() == "kubeconfig" || cmd.Parent() != nil && cmd.Parent().Name() == "kubeconfig" {
				return nil
			}
			var err error
			cs, err = client.New(kubeconfigPath)
			if err != nil {
				output.Errorf(os.Stderr, "could not connect to cluster: %s", err)
				os.Exit(1)
			}
			return nil
		},
	}

	// Replaced by kelper's own completion command, which supports --output.
	root.CompletionOptions.DisableDefaultCmd = true

	root.PersistentFlags().StringVar(&kubeconfigPath, "kubeconfig", "", "path to kubeconfig file (default: $KUBECONFIG or ~/.kube/config)")

	// Register feature subcommands.
	root.AddCommand(healthcheck.Command(&cs))
	root.AddCommand(images.Command(&cs))
	root.AddCommand(resources.Command(&cs))
	root.AddCommand(volumes.Command(&cs))
	root.AddCommand(kubeconfig.Command(&kubeconfigPath))
	root.AddCommand(completion.Command())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// firstCommand returns the first non-flag argument and its index, skipping
// kelper's own global flags and their values. It returns "" and len(args) when
// args holds nothing but flags.
func firstCommand(args []string) (string, int) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return a, i
		}
		// --kubeconfig takes its value as a separate argument; the
		// --kubeconfig=<path> form does not.
		if a == "--kubeconfig" {
			i++
		}
	}
	return "", len(args)
}

// extractKubeconfigFlag returns the path given by a --kubeconfig flag among
// args, or "" when the flag is absent.
func extractKubeconfigFlag(args []string) string {
	for i, a := range args {
		if path, ok := strings.CutPrefix(a, "--kubeconfig="); ok {
			return path
		}
		if a == "--kubeconfig" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// isYAMLOutput returns true if args contain -o yaml or -oyaml.
func isYAMLOutput(args []string) bool {
	joined := strings.Join(args, " ")
	return strings.Contains(joined, "-o yaml") || strings.Contains(joined, "-oyaml")
}

// hasFlag returns true if args contain the given flag string.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
