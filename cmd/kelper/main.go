package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/jthunderbird/kelper/internal/client"
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
}

func main() {
	var kubeconfigPath string
	var cs *kubernetes.Clientset

	// Fast-path: if the first non-flag argument is not a kelper-native
	// subcommand (and not a help/version flag), forward everything directly to
	// kubectl without involving cobra's subcommand routing.
	args := os.Args[1:]
	if len(args) > 0 {
		first := args[0]
		// Handle version flags up front.
		if first == "-v" || first == "--version" {
			fmt.Printf("kelper %s\n", appVersion)
			return
		}
		// Strip leading dashes to find the base flag name (e.g. --help → help).
		isHelpFlag := first == "--help" || first == "-h"
		if !isHelpFlag && !kelperSubcommands[first] {
			// Intercept: get -o yaml (and not --raw)
			if first == "get" && isYAMLOutput(args) && !hasFlag(args, "--raw") {
				// Client init needed for get interception.
				var err error
				cs, err = client.New(kubeconfigPath)
				if err != nil {
					output.Errorf(os.Stderr, "could not connect to cluster: %s", err)
					os.Exit(1)
				}
				if err := get.Run(cs, args); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
				return
			}
			// Everything else: transparent passthrough to kubectl.
			if err := passthrough.Run(args); err != nil {
				os.Exit(1)
			}
			return
		}
	}

	root := &cobra.Command{
		Use:          "kelper",
		Short:        "kubectl wrapper with enhanced output and interactivity",
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
			// Skip client init for completion and help — these don't need a cluster.
			if cmd.Name() == "completion" || cmd.Name() == "help" {
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

	root.PersistentFlags().StringVar(&kubeconfigPath, "kubeconfig", "", "path to kubeconfig file (default: $KUBECONFIG or ~/.kube/config)")

	// Register feature subcommands.
	root.AddCommand(healthcheck.Command(&cs))
	root.AddCommand(images.Command(&cs))
	root.AddCommand(resources.Command(&cs))
	root.AddCommand(volumes.Command(&cs))
	root.AddCommand(kubeconfig.Command())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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


