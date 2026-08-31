// Package cmd builds kelper's healthcheck cobra command.
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jthunderbird/kelper/internal/client"
	"github.com/jthunderbird/kelper/internal/health"
	"github.com/jthunderbird/kelper/internal/health/dashboard"
	"github.com/jthunderbird/kelper/internal/health/engine"
	"github.com/jthunderbird/kelper/internal/health/render"
	"github.com/spf13/cobra"
)

// Command returns the healthcheck command. kubeconfigPath points at the value
// bound to kelper's global --kubeconfig flag, which is parsed after this
// function runs.
func Command(kubeconfigPath *string) *cobra.Command {
	var (
		namespace     string
		allNamespaces bool
		detail        bool
		noColor       bool
		listChecks    bool
		include       []string
		exclude       []string
		severity      string
		watch         bool
		interval      time.Duration
		scanCRDs      bool
		reportMissing bool
		limitPct      float64
		pvcPct        float64
		nodePct       float64
		restartWindow time.Duration
		jobWindow     time.Duration
		timeout       time.Duration
	)

	cmd := &cobra.Command{
		Use:     "healthcheck",
		Aliases: []string{"health"},
		Short:   "Diagnose cluster health",
		Long: "Run kelper's health checks against the cluster.\n\n" +
			"With no target flags it opens a live dashboard. With -n or -A it prints\n" +
			"tables and exits non-zero when problems are found.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if listChecks {
				printChecks(cmd)
				return nil
			}

			minSeverity := health.SeverityOk
			if severity != "" {
				s, err := health.ParseSeverity(severity)
				if err != nil {
					return err
				}
				minSeverity = s
			}

			ns := namespace
			if allNamespaces {
				ns = ""
			}

			opts := engine.Options{
				Namespace:           ns,
				Include:             include,
				Exclude:             exclude,
				MinSeverity:         minSeverity,
				LimitThreshold:      limitPct,
				PVCThreshold:        pvcPct,
				NodeThreshold:       nodePct,
				RestartWindow:       restartWindow,
				JobWindow:           jobWindow,
				ScanCRDs:            scanCRDs,
				ReportMissingLimits: reportMissing,
				Timeout:             timeout,
			}

			bundle, err := client.NewBundle(*kubeconfigPath)
			if err != nil {
				return fmt.Errorf("could not connect to cluster: %w", err)
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			eng := engine.NewEngine(ctx, bundle, opts)

			// No explicit target means the interactive dashboard, matching the
			// behavior kelper has always had for a bare healthcheck.
			interactive := !allNamespaces && namespace == "" && !watch
			switch {
			case interactive:
				return dashboard.Run(ctx, eng, dashboard.Options{Interval: interval})
			case watch:
				return runWatch(ctx, eng, interval, render.Options{Detail: detail, NoColor: noColor})
			}

			res, _, err := eng.Run(ctx)
			if err != nil {
				return err
			}
			render.Table(cmd.OutOrStdout(), res, render.Options{Detail: detail, NoColor: noColor})

			// Exit non-zero when anything at warning level or above is found,
			// so the command is usable as a gate.
			if max, ok := res.MaxSeverity(); ok && max >= health.SeverityWarn {
				return errUnhealthy{max}
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVarP(&namespace, "namespace", "n", "", "namespace to check")
	f.BoolVarP(&allNamespaces, "all-namespaces", "A", false, "check all namespaces")
	f.BoolVar(&detail, "detail", false, "expand evidence and remediation for each finding")
	f.BoolVar(&noColor, "no-color", false, "disable colored output")
	f.BoolVar(&listChecks, "list-checks", false, "list the available checks and exit")
	f.StringSliceVar(&include, "checks", nil, "only run these checks (comma separated)")
	f.StringSliceVar(&exclude, "skip", nil, "skip these checks (comma separated)")
	f.StringVar(&severity, "severity", "", "only report findings at this severity or above (info|warn|error|critical)")
	f.BoolVarP(&watch, "watch", "w", false, "run continuously, printing only what changed")
	f.DurationVar(&interval, "interval", 30*time.Second, "refresh interval for --watch and the dashboard")
	f.BoolVar(&scanCRDs, "crds", true, "scan custom resources for failing status conditions")
	f.BoolVar(&reportMissing, "report-missing-limits", false, "report containers with no resource requests or limits")
	f.Float64Var(&limitPct, "limit-threshold", 85, "percent of a container limit at which to warn")
	f.Float64Var(&pvcPct, "pvc-threshold", 85, "percent of volume capacity at which to warn")
	f.Float64Var(&nodePct, "node-threshold", 85, "percent of node allocatable at which to warn")
	f.DurationVar(&restartWindow, "restart-window", 0, "how recently a restart counts as active churn (default 10m)")
	f.DurationVar(&jobWindow, "job-window", 0, "how recently a Job failure is still reported (default 24h)")
	f.DurationVar(&timeout, "timeout", 2*time.Minute, "overall timeout for a single run")

	return cmd
}

// errUnhealthy carries the exit signal without printing a second error line,
// since the findings themselves have already been rendered.
type errUnhealthy struct{ sev health.Severity }

func (e errUnhealthy) Error() string {
	return fmt.Sprintf("unhealthy: highest severity %s", e.sev)
}

// printChecks lists the registered checks and what each needs.
func printChecks(cmd *cobra.Command) {
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, "Available checks:")
	for _, c := range engine.DefaultRegistry(engine.Options{}).All() {
		req := ""
		if caps := c.Requires(); len(caps) > 0 {
			strs := make([]string, len(caps))
			for i, cp := range caps {
				strs[i] = string(cp)
			}
			req = fmt.Sprintf("  (requires %s)", strings.Join(strs, ", "))
		}
		fmt.Fprintf(w, "  %-14s %s%s\n", c.Name(), c.Description(), req)
	}
}

// runWatch polls on an interval and prints only what changed since the previous
// run, so a long-running watch stays readable.
func runWatch(ctx context.Context, eng *engine.Engine, interval time.Duration, ropts render.Options) error {
	hist := health.NewHistory()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		res, _, err := eng.Run(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintf(os.Stderr, "run failed: %s\n", err)
		} else {
			render.Changes(os.Stdout, hist.Observe(res), ropts)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
