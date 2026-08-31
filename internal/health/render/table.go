// Package render turns health findings into terminal output. It consumes only
// the finding model, never Kubernetes types, so the same findings can be shown
// by the CLI, the dashboard or the watch differ.
package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/jthunderbird/kelper/internal/health"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
)

var (
	criticalStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	warnStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	infoStyle     = lipgloss.NewStyle().Faint(true)
	okStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	headingStyle  = lipgloss.NewStyle().Bold(true)
	dimStyle      = lipgloss.NewStyle().Faint(true)
	detailStyle   = lipgloss.NewStyle().Faint(true).Italic(true)
)

// SeverityStyle returns the lipgloss style for a severity.
func SeverityStyle(s health.Severity) lipgloss.Style {
	switch s {
	case health.SeverityCritical:
		return criticalStyle
	case health.SeverityError:
		return errorStyle
	case health.SeverityWarn:
		return warnStyle
	case health.SeverityInfo:
		return infoStyle
	default:
		return okStyle
	}
}

// Options controls how much detail the table renderer emits.
type Options struct {
	// Detail expands evidence and remediation under each finding.
	Detail bool
	// NoColor disables styling, for pipes and CI logs.
	NoColor bool
}

// Table writes findings grouped by severity, followed by a summary.
func Table(w io.Writer, res health.Result, opts Options) {
	if opts.NoColor {
		lipgloss.SetColorProfile(0)
	}

	if len(res.Findings) == 0 {
		fmt.Fprintln(w, okStyle.Render("No health problems found."))
	} else {
		bySeverity := map[health.Severity][]health.Finding{}
		for _, f := range res.Findings {
			bySeverity[f.Severity] = append(bySeverity[f.Severity], f)
		}
		order := []health.Severity{
			health.SeverityCritical, health.SeverityError,
			health.SeverityWarn, health.SeverityInfo,
		}
		for _, sev := range order {
			fs := bySeverity[sev]
			if len(fs) == 0 {
				continue
			}
			style := SeverityStyle(sev)
			fmt.Fprintf(w, "%s\n", style.Render(fmt.Sprintf("%s (%d)", sev, len(fs))))
			writeGroup(w, fs, opts)
			fmt.Fprintln(w)
		}
	}

	writeSummary(w, res)
}

// noBorders matches the borderless table style used elsewhere in kelper.
func noBorders() tablewriter.Option {
	return tablewriter.WithBorders(tw.Border{
		Left: tw.Off, Right: tw.Off, Top: tw.Off, Bottom: tw.Off,
	})
}

func writeGroup(w io.Writer, fs []health.Finding, opts Options) {
	if opts.Detail {
		for _, f := range fs {
			writeDetailed(w, f)
		}
		return
	}
	t := tablewriter.NewTable(w, noBorders())
	t.Header([]string{"NAMESPACE", "KIND", "NAME", "REASON", "MESSAGE"})
	for _, f := range fs {
		ns := f.Ref.Namespace
		if ns == "" {
			ns = "-"
		}
		t.Append([]string{ns, f.Ref.Kind, f.Ref.Name, f.Reason, f.Message})
	}
	t.Render()
}

// writeDetailed renders one finding with its evidence and remediation.
func writeDetailed(w io.Writer, f health.Finding) {
	style := SeverityStyle(f.Severity)
	fmt.Fprintf(w, "  %s %s\n", style.Render("●"), headingStyle.Render(f.Ref.String()))
	fmt.Fprintf(w, "    %s  %s\n", style.Render(f.Reason), f.Message)
	// Detail often restates the condition message verbatim; only show it when
	// it adds something.
	if f.Detail != "" && f.Detail != f.Message {
		fmt.Fprintf(w, "    %s\n", detailStyle.Render(f.Detail))
	}
	for _, e := range f.Evidence {
		if e.Value == "" {
			continue
		}
		fmt.Fprintf(w, "    %s %s\n", dimStyle.Render(e.Label+":"), truncate(e.Value, 100))
	}
	if f.Remediation != "" {
		fmt.Fprintf(w, "    %s %s\n", dimStyle.Render("fix:"), f.Remediation)
	}
	fmt.Fprintln(w)
}

func writeSummary(w io.Writer, res health.Result) {
	counts := health.Tally(res.Findings)
	var parts []string
	for _, sev := range []health.Severity{
		health.SeverityCritical, health.SeverityError,
		health.SeverityWarn, health.SeverityInfo,
	} {
		if n := counts[sev]; n > 0 {
			parts = append(parts, SeverityStyle(sev).Render(fmt.Sprintf("%d %s", n, sev)))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, okStyle.Render("all clear"))
	}
	fmt.Fprintf(w, "Summary: %s\n", strings.Join(parts, ", "))

	// Degraded sources are reported as one line rather than as errors: kelper
	// must stay useful against clusters without metrics-server or node proxy.
	if len(res.Degraded) > 0 {
		fmt.Fprintf(w, "%s\n", dimStyle.Render(
			fmt.Sprintf("Degraded: %s", strings.Join(res.Degraded, "; "))))
	}
	if len(res.Skipped) > 0 {
		var names []string
		for name, reason := range res.Skipped {
			names = append(names, fmt.Sprintf("%s (%s)", name, reason))
		}
		fmt.Fprintf(w, "%s\n", dimStyle.Render(
			fmt.Sprintf("Skipped %d check(s): %s", len(res.Skipped), strings.Join(names, ", "))))
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
