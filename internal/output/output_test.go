package output_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/output"
)

func TestSeparatorLineLength(t *testing.T) {
	sep := output.Separator(20)
	// strip ANSI codes for length check — lipgloss faint adds \x1b[2m...\x1b[0m
	plain := sep
	// Remove all ANSI escape sequences
	for strings.Contains(plain, "\x1b[") {
		start := strings.Index(plain, "\x1b[")
		end := strings.Index(plain[start:], "m")
		if end == -1 {
			break
		}
		plain = plain[:start] + plain[start+end+1:]
	}
	if len([]rune(plain)) != 20 {
		t.Errorf("expected separator of 20 runes, got %d: %q", len([]rune(plain)), plain)
	}
}

func TestPodHeaderContainsPodName(t *testing.T) {
	var buf bytes.Buffer
	output.PodHeader(&buf, "mypod", "mynamespace")
	out := buf.String()
	if !strings.Contains(out, "mypod") {
		t.Errorf("pod header missing pod name, got: %s", out)
	}
	if !strings.Contains(out, "mynamespace") {
		t.Errorf("pod header missing namespace, got: %s", out)
	}
}

func TestErrorfWritesToWriter(t *testing.T) {
	var buf bytes.Buffer
	output.Errorf(&buf, "something went wrong: %s", "details")
	out := buf.String()
	if !strings.Contains(out, "error:") {
		t.Errorf("expected 'error:' prefix, got: %s", out)
	}
	if !strings.Contains(out, "something went wrong: details") {
		t.Errorf("expected error message in output, got: %s", out)
	}
}
