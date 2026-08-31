package render

import (
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/jthunderbird/kelper/internal/health"
)

var (
	newStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	resolvedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	flapStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
)

// Changes writes only what changed between runs. Persistent findings are
// omitted so a long-running watch does not reprint the same list forever.
func Changes(w io.Writer, changes []health.Change, opts Options) {
	if opts.NoColor {
		lipgloss.SetColorProfile(0)
	}

	var printed int
	stamp := time.Now().Format("15:04:05")
	for _, c := range changes {
		var marker string
		switch c.Delta {
		case health.DeltaNew:
			marker = newStyle.Render("+ NEW     ")
		case health.DeltaResolved:
			marker = resolvedStyle.Render("- RESOLVED")
		default:
			continue
		}

		flap := ""
		if c.Flapping {
			flap = flapStyle.Render("  [flapping]")
		}
		fmt.Fprintf(w, "%s %s %s %s  %s%s\n",
			dimStyle.Render(stamp), marker,
			SeverityStyle(c.Finding.Severity).Render(c.Finding.Severity.String()),
			c.Finding.Ref.String(), c.Finding.Message, flap)

		if opts.Detail && c.Delta == health.DeltaNew {
			for _, e := range c.Finding.Evidence {
				if e.Value == "" {
					continue
				}
				fmt.Fprintf(w, "             %s %s\n",
					dimStyle.Render(e.Label+":"), truncate(e.Value, 100))
			}
			if c.Finding.Remediation != "" {
				fmt.Fprintf(w, "             %s %s\n",
					dimStyle.Render("fix:"), c.Finding.Remediation)
			}
		}
		printed++
	}

	if printed == 0 {
		fmt.Fprintf(w, "%s\n", dimStyle.Render(stamp+" no change"))
	}
}
