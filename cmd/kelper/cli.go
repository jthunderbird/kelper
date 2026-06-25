package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
)

// appVersion is overridden at build time via -ldflags "-X main.appVersion=...".
var appVersion = "dev"

// globalOpts holds flags that apply before the command, shared by every command
// and the kubectl passthrough.
type globalOpts struct {
	kubeconfig string
}

// command is a single native kelper subcommand.
type command struct {
	name     string
	summary  string         // one-line description for the command list
	usage    string         // usage line shown in command help
	flagDocs [][2]string    // {flag, description} pairs rendered in command help
	examples []string       // example invocations
	flags    func(*flag.FlagSet) // registers the command's real flags for parsing
	run      func(*globalOpts, []string)
}

// newFlagSet builds a parser for the command whose -h/--help renders kelper's
// own command help.
func (c *command) newFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("kelper "+c.name, flag.ExitOnError)
	if c.flags != nil {
		c.flags(fs)
	}
	fs.Usage = func() { printCommandHelp(os.Stdout, c) }
	return fs
}

// commands returns the registry of native subcommands, in display order.
func commands() []*command {
	return []*command{
		listPodsCommand(),
		kubectlCommand(),
		versionCommand(),
		helpCommand(),
	}
}

func lookupCommand(name string) (*command, bool) {
	for _, c := range commands() {
		if c.name == name {
			return c, true
		}
	}
	return nil, false
}

func listPodsCommand() *command {
	var namespace, kubeconfig string
	c := &command{
		name:    "list-pods",
		summary: "List pods with their init containers, containers, and images",
		usage:   "kelper list-pods [flags]",
		flagDocs: [][2]string{
			{"-n, --namespace string", "Namespace to list pods from (default \"default\")"},
			{"--kubeconfig string", "Path to the kubeconfig file"},
		},
		examples: []string{
			"kelper list-pods",
			"kelper list-pods -n kube-system",
		},
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&namespace, "namespace", "default", "Namespace to list pods from")
			fs.StringVar(&namespace, "n", "default", "Namespace to list pods from (shorthand)")
			fs.StringVar(&kubeconfig, "kubeconfig", "", "Path to the kubeconfig file")
		},
	}
	c.run = func(g *globalOpts, args []string) {
		_ = c.newFlagSet().Parse(args)
		kc := kubeconfig
		if kc == "" {
			kc = g.kubeconfig
		}
		resolved, cleanup, err := resolveKubeconfig(kc)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer cleanup()
		listAllPods(resolved, namespace)
	}
	return c
}

func kubectlCommand() *command {
	c := &command{
		name:    "kubectl",
		summary: "Run a raw kubectl command through kelper (explicit passthrough)",
		usage:   "kelper kubectl [--] <kubectl args>",
		examples: []string{
			"kelper kubectl get nodes -o wide",
			"kelper kubectl -- --all-namespaces get pods",
		},
	}
	c.run = func(g *globalOpts, args []string) {
		if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
			printCommandHelp(os.Stdout, c)
			return
		}
		// Accept an optional "--" separator so kubectl flags can lead.
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		if len(args) == 0 {
			printCommandHelp(os.Stdout, c)
			return
		}
		runKubectlPassthrough(g, args)
	}
	return c
}

func versionCommand() *command {
	c := &command{
		name:    "version",
		summary: "Print the kelper version",
		usage:   "kelper version",
	}
	c.run = func(g *globalOpts, args []string) {
		fmt.Printf("kelper %s\n", appVersion)
	}
	return c
}

func helpCommand() *command {
	c := &command{
		name:     "help",
		summary:  "Show help for kelper or a specific command",
		usage:    "kelper help [command]",
		examples: []string{"kelper help", "kelper help list-pods"},
	}
	c.run = func(g *globalOpts, args []string) {
		if len(args) == 0 {
			printRootHelp(os.Stdout)
			return
		}
		if target, ok := lookupCommand(args[0]); ok {
			printCommandHelp(os.Stdout, target)
			return
		}
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		printRootHelp(os.Stderr)
		os.Exit(1)
	}
	return c
}

// printRootHelp renders the top-level help.
func printRootHelp(w io.Writer) {
	fmt.Fprint(w, "kelper - a friendlier kubectl wrapper with built-in api-server failover\n\n")
	fmt.Fprint(w, "USAGE:\n")
	fmt.Fprint(w, "  kelper [global flags] <command> [args]\n")
	fmt.Fprint(w, "  kelper [global flags] <kubectl args>   # unrecognized input is passed to kubectl\n\n")

	fmt.Fprint(w, "COMMANDS:\n")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range commands() {
		fmt.Fprintf(tw, "  %s\t%s\n", c.name, c.summary)
	}
	tw.Flush()

	fmt.Fprint(w, "\nGLOBAL FLAGS:\n")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  %s\t%s\n", "--kubeconfig string", "Path to the kubeconfig file. Accepts a comma-delimited")
	fmt.Fprintf(tw, "  %s\t%s\n", "", "list of api-server endpoints for client-side failover.")
	fmt.Fprintf(tw, "  %s\t%s\n", "-h, --help", "Show this help")
	tw.Flush()

	fmt.Fprint(w, "\nPASSTHROUGH:\n")
	fmt.Fprint(w, "  Any input kelper does not recognize is forwarded to kubectl. On '-o yaml'\n")
	fmt.Fprint(w, "  output, Secrets are base64-decoded and noisy fields (uid, creationTimestamp,\n")
	fmt.Fprint(w, "  status) are stripped automatically.\n")

	fmt.Fprint(w, "\nEXAMPLES:\n")
	for _, e := range []string{
		"kelper get pods -A",
		"kelper get secret -n kube-system root-ca -o yaml",
		"kelper list-pods --namespace kube-system",
		"kelper kubectl -- get nodes -o wide",
	} {
		fmt.Fprintf(w, "  %s\n", e)
	}

	fmt.Fprint(w, "\nRun 'kelper help <command>' for more information on a command.\n")
}

// printCommandHelp renders the help for a single command.
func printCommandHelp(w io.Writer, c *command) {
	fmt.Fprintf(w, "%s - %s\n\n", c.name, c.summary)
	fmt.Fprintf(w, "USAGE:\n  %s\n", c.usage)

	fmt.Fprint(w, "\nFLAGS:\n")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, fd := range c.flagDocs {
		fmt.Fprintf(tw, "  %s\t%s\n", fd[0], fd[1])
	}
	fmt.Fprintf(tw, "  %s\t%s\n", "-h, --help", "Show this help")
	tw.Flush()

	if len(c.examples) > 0 {
		fmt.Fprint(w, "\nEXAMPLES:\n")
		for _, e := range c.examples {
			fmt.Fprintf(w, "  %s\n", e)
		}
	}
}
