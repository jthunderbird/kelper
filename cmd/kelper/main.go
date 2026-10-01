package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jthunderbird/kelper/internal/client"
	"github.com/jthunderbird/kelper/internal/completion"
	"github.com/jthunderbird/kelper/internal/get"
	healthcmd "github.com/jthunderbird/kelper/internal/health/cmd"
	"github.com/jthunderbird/kelper/internal/images"
	"github.com/jthunderbird/kelper/internal/k9s"
	"github.com/jthunderbird/kelper/internal/kubeconfig"
	"github.com/jthunderbird/kelper/internal/output"
	"github.com/jthunderbird/kelper/internal/passthrough"
	"github.com/jthunderbird/kelper/internal/pull"
	"github.com/jthunderbird/kelper/internal/resources"
	"github.com/jthunderbird/kelper/internal/volumes"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// appVersion is overridden at build time via -ldflags "-X main.appVersion=...".
var appVersion = "dev"

// kelperSubcommands is the set of first-level subcommands that kelper handles
// natively. Everything else is transparently forwarded to kubectl.
var kelperSubcommands = map[string]bool{
	"healthcheck": true,
	"health":      true,
	"k9s":         true,
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
	"pull":        true,
	"completion":  true,
	"help":        true,
	// Cobra's hidden completion callbacks. The generated shell scripts invoke
	// these on every keystroke; without them the fast-path below would forward
	// the request to kubectl and completion would silently do nothing.
	"__complete":       true,
	"__completeNoDesc": true,
}

func main() {
	silenceKlog()

	var kubeconfigPath string
	var cs kubernetes.Interface

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
			// --kubeconfig is consumed here so failover can resolve it, and is
			// re-applied out of band. Any other leading flags (-n, --context,
			// ...) are kubectl's and are forwarded in place.
			globalPath, leading := splitKubeconfigFlag(args[:cmdIndex])
			forwarded := append(leading, args[cmdIndex:]...)
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
			// healthcheck, k9s and pull build their own clients, so the
			// shared clientset is not needed and its failure should not
			// block them.
			case "healthcheck", "health", "k9s", "pull":
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
	root.AddCommand(healthcmd.Command(&kubeconfigPath))
	root.AddCommand(k9sCommand())
	root.AddCommand(images.Command(&cs))
	root.AddCommand(resources.Command(&cs))
	root.AddCommand(volumes.Command(&cs))
	root.AddCommand(kubeconfig.Command(&kubeconfigPath))
	root.AddCommand(pull.Command(&kubeconfigPath))
	root.AddCommand(completion.Command())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// valueFlags are the kubectl global flags (plus kelper's --kubeconfig) whose
// value may follow as a separate argument. firstCommand must skip that value,
// or `kelper -n kube-system get pods` would route on "kube-system".
var valueFlags = map[string]bool{
	"--kubeconfig":            true,
	"-n":                      true,
	"--namespace":             true,
	"--context":               true,
	"--cluster":               true,
	"--user":                  true,
	"-s":                      true,
	"--server":                true,
	"--token":                 true,
	"--as":                    true,
	"--as-group":              true,
	"--as-uid":                true,
	"--certificate-authority": true,
	"--client-certificate":    true,
	"--client-key":            true,
	"--tls-server-name":       true,
	"--request-timeout":       true,
	"--cache-dir":             true,
	"--password":              true,
	"--username":              true,
	"--profile":               true,
	"--profile-output":        true,
	"-v":                      true,
	"--v":                     true,
	"--vmodule":               true,
	"--log-file":              true,
	"--log-dir":               true,
}

// firstCommand returns the first non-flag argument and its index, skipping
// leading global flags and their values. It returns "" and len(args) when args
// holds nothing but flags.
func firstCommand(args []string) (string, int) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return a, i
		}
		// The --flag=value form carries its own value; the separate form
		// consumes the next argument.
		if valueFlags[a] {
			i++
		}
	}
	return "", len(args)
}

// splitKubeconfigFlag separates a --kubeconfig flag from the other leading
// flags in args, returning its path ("" when absent) and the remaining flags.
func splitKubeconfigFlag(args []string) (string, []string) {
	var path string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if p, ok := strings.CutPrefix(a, "--kubeconfig="); ok {
			path = p
			continue
		}
		if a == "--kubeconfig" {
			if i+1 < len(args) {
				path = args[i+1]
				i++
			}
			continue
		}
		rest = append(rest, a)
	}
	return path, rest
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

// k9sCommand launches the embedded k9s. Flag parsing is disabled so that every
// argument reaches k9s rather than being interpreted by kelper's cobra root.
func k9sCommand() *cobra.Command {
	return &cobra.Command{
		Use:                "k9s [flags]",
		Short:              "Launch k9s, the Kubernetes terminal UI",
		DisableFlagParsing: true,
		Run: func(_ *cobra.Command, args []string) {
			k9s.Run(args)
		},
	}
}

// silenceKlog stops client-go's logger from writing operational notices, such
// as client-side throttling delays, into kelper's output. Real errors are
// surfaced through returned errors instead.
func silenceKlog() {
	fs := flag.NewFlagSet("klog", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	klog.InitFlags(fs)
	_ = fs.Parse([]string{"-logtostderr=false", "-alsologtostderr=false", "-stderrthreshold=FATAL", "-v=0"})
	klog.SetOutput(io.Discard)
}
