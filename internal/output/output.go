package output

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	podHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("33"))
	keyLabelStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("36"))
	dimStyle       = lipgloss.NewStyle().Faint(true)
	errorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// Separator returns a unicode rule line of n runes in a dim style.
func Separator(n int) string {
	return dimStyle.Render(strings.Repeat("─", n))
}

// PodHeader writes a colored pod name + namespace header followed by a separator.
func PodHeader(w io.Writer, podName, namespace string) {
	header := fmt.Sprintf("pod: %s (-n %s)", podName, namespace)
	fmt.Fprintln(w, podHeaderStyle.Render(header))
	fmt.Fprintln(w, Separator(len(header)))
	fmt.Fprintln(w)
}

// KeyLabel returns a highlighted key label for secret output.
func KeyLabel(key string) string {
	return keyLabelStyle.Render("KEY: " + key)
}

// Errorf writes a formatted error message with "error: " prefix to w.
func Errorf(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w, errorStyle.Render("error: "+msg))
}
