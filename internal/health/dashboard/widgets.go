package dashboard

import (
	"fmt"
	"strings"
	"time"

	"github.com/derailed/tcell/v2"
	"github.com/jthunderbird/kelper/internal/health"
)

// sparkBlocks are the eighth-height blocks used to draw trend lines.
var sparkBlocks = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// sparkline renders a series as a compact trend, showing the most recent width
// samples. A flat zero series renders as a baseline rather than nothing.
func sparkline(series []float64, width int) string {
	if len(series) == 0 {
		return ""
	}
	if len(series) > width {
		series = series[len(series)-width:]
	}
	max := 0.0
	for _, v := range series {
		if v > max {
			max = v
		}
	}
	var b strings.Builder
	for _, v := range series {
		if max <= 0 {
			b.WriteRune(sparkBlocks[0])
			continue
		}
		idx := int(v / max * float64(len(sparkBlocks)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkBlocks) {
			idx = len(sparkBlocks) - 1
		}
		b.WriteRune(sparkBlocks[idx])
	}
	return b.String()
}

// bar renders a percentage as a colored horizontal meter.
func bar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100 * float64(width))
	color := "green"
	switch {
	case pct >= 90:
		color = "red"
	case pct >= 75:
		color = "yellow"
	}
	return fmt.Sprintf("[%s::]%s[gray::]%s[-:-:-] %3.0f%%",
		color, strings.Repeat("█", filled),
		strings.Repeat("░", width-filled), pct)
}

// sevCount renders one severity counter, dimmed when zero so the eye lands on
// what is actually firing.
func sevCount(label string, n int, color string) string {
	if n == 0 {
		return fmt.Sprintf("[gray::]%s 0[-:-:-]", label)
	}
	return fmt.Sprintf("[%s::b]%s %d[-:-:-]", color, label, n)
}

// severityColor maps a severity to its table color.
func severityColor(s health.Severity) tcell.Color {
	switch s {
	case health.SeverityCritical:
		return tcell.ColorRed
	case health.SeverityError:
		return tcell.ColorIndianRed
	case health.SeverityWarn:
		return tcell.ColorYellow
	case health.SeverityInfo:
		return tcell.ColorGray
	default:
		return tcell.ColorGreen
	}
}

// colorName maps a severity to a tview color tag.
func colorName(s health.Severity) string {
	switch s {
	case health.SeverityCritical, health.SeverityError:
		return "red"
	case health.SeverityWarn:
		return "yellow"
	case health.SeverityInfo:
		return "gray"
	default:
		return "green"
	}
}

// filter narrows findings by free text and minimum severity. Matching is
// case-insensitive across the fields a user is likely to type.
func filter(fs []health.Finding, text string, min health.Severity) []health.Finding {
	text = strings.ToLower(strings.TrimSpace(text))
	out := make([]health.Finding, 0, len(fs))
	for _, f := range fs {
		if f.Severity < min {
			continue
		}
		if text != "" && !matches(f, text) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func matches(f health.Finding, text string) bool {
	for _, field := range []string{
		f.Ref.Namespace, f.Ref.Name, f.Ref.Kind, f.Reason, f.Message, f.Check, f.Ref.Node,
	} {
		if strings.Contains(strings.ToLower(field), text) {
			return true
		}
	}
	return false
}

// age renders how long ago t was, compactly.
func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// truncate shortens s to n runes, collapsing newlines.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	if n < 4 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
