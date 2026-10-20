package all

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jthunderbird/kelper/internal/client"
	"github.com/jthunderbird/kelper/internal/output"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const longHelp = `List every resource type the cluster serves, custom resources included,
which "kubectl get all" does not.

With -n NAMESPACE only that namespace's namespaced types are listed. With -A, or
with no namespace flag at all (there is no default namespace here), every type is
listed, cluster-scoped ones too. That output is very large, so on a terminal kelper
warns and asks first; pipe to a file, or pass -y, to skip the question.

Events and leases are skipped unless named with --include or --include-noisy. Types
that cannot be listed (permissions, a broken aggregated API) are named on a final
"Skipped:" line.

Output: a table per type (-o wide for extra columns), -o name for one object per
line, or -o yaml / -o json for a single List. YAML is cleaned like "kelper get -o
yaml"; Secret values are not decoded.`

// Command returns the all command. kubeconfigPath points at the root
// --kubeconfig flag's value, which is read once the command runs.
func Command(kubeconfigPath *string) *cobra.Command {
	var (
		namespace     string
		allNamespaces bool
		o             Options
		yes           bool
	)
	cmd := &cobra.Command{
		Use:   "all",
		Short: "List every resource type on the cluster, custom resources included",
		Long:  longHelp,
		Example: `  kelper all -n kube-system
  kelper all -A -y > cluster.txt
  kelper all -n prod -l app=web --exclude secrets
  kelper all --include certificates,issuers -o yaml`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if namespace != "" && !allNamespaces {
				o.Namespace = namespace
			}
			switch o.Output {
			case "", "wide", "name", "yaml", "json":
			default:
				return fmt.Errorf("unsupported output %q (use wide, name, yaml or json)", o.Output)
			}
			stdout := cmd.OutOrStdout()
			if o.Namespace == "" && !yes {
				if f, ok := stdout.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
					ok, err := confirmAll(cmd.InOrStdin(), cmd.ErrOrStderr())
					if err != nil {
						return err
					}
					if !ok {
						fmt.Fprintln(cmd.ErrOrStderr(), "Cancelled.")
						return nil
					}
				}
			}
			cfg, err := client.RestConfig(*kubeconfigPath)
			if err != nil {
				return fmt.Errorf("could not connect to cluster: %w", err)
			}
			cfg.QPS, cfg.Burst = 50, 100
			src, err := NewAPISource(cfg)
			if err != nil {
				return err
			}
			return Run(cmd.Context(), src, o, stdout, cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "list only this namespace's namespaced types")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "every namespace and every type (also the default)")
	cmd.Flags().StringVarP(&o.Selector, "selector", "l", "", "label selector applied to every type")
	cmd.Flags().StringSliceVar(&o.Include, "include", nil, "only these types (names, resource.group, kinds or short names, comma separated)")
	cmd.Flags().StringSliceVar(&o.Exclude, "exclude", nil, "skip these types")
	cmd.Flags().BoolVar(&o.IncludeNoisy, "include-noisy", false, "also list events and leases")
	cmd.Flags().StringVarP(&o.Output, "output", "o", "", "output form: wide, name, yaml or json (default: a table per type)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask before listing everything")
	return cmd
}

// confirmAll warns about the size of a whole-cluster listing and asks.
func confirmAll(in io.Reader, errOut io.Writer) (bool, error) {
	fmt.Fprintln(errOut, "kelper all will list every resource type in every namespace, including cluster-scoped")
	fmt.Fprintln(errOut, "ones. That is usually a very large amount of output; consider redirecting it to a file")
	fmt.Fprintln(errOut, "(kelper all > cluster.txt) or narrowing with -n NAMESPACE.")
	fmt.Fprint(errOut, "Continue? [y/N]: ")
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// Run discovers, selects, lists and renders. It fails only when nothing could
// be listed; individual failures end up on a "Skipped:" line.
func Run(ctx context.Context, src Source, o Options, out, errOut io.Writer) error {
	all, derr := src.Resources(ctx)
	types := Select(all, o)
	if len(types) == 0 {
		if derr != nil {
			return fmt.Errorf("discovery failed: %w", derr)
		}
		return errors.New("no matching resource types")
	}
	results := Fetch(ctx, src, types, o)
	skipped := Skipped(results)
	if len(skipped) == len(results) {
		return fmt.Errorf("could not list any resource type: %s", skipped[0])
	}
	if err := Render(out, results, o, output.Heading); err != nil {
		return err
	}
	if o.Output != "yaml" && o.Output != "json" && countRows(results) == 0 {
		if o.Namespace != "" {
			fmt.Fprintf(errOut, "No resources found in %s namespace.\n", o.Namespace)
		} else {
			fmt.Fprintln(errOut, "No resources found.")
		}
	}
	if len(skipped) > 0 {
		fmt.Fprintf(errOut, "Skipped: %s\n", strings.Join(skipped, ", "))
	}
	if derr != nil {
		fmt.Fprintf(errOut, "Degraded discovery: %s\n", short(derr))
	}
	return nil
}

func countRows(results []result) int {
	n := 0
	for _, r := range results {
		if r.table != nil {
			n += len(r.table.Rows)
		}
	}
	return n
}
