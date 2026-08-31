package dashboard

import (
	"strings"
	"testing"
	"time"

	"github.com/jthunderbird/kelper/internal/health"
)

func mkFinding(ns, name, reason string, sev health.Severity) health.Finding {
	return health.Finding{
		Check:    "pods",
		Severity: sev,
		Ref:      health.ResourceRef{Kind: "Pod", Namespace: ns, Name: name},
		Reason:   reason,
		Message:  "something happened to " + name,
	}
}

func TestFilterBySeverity(t *testing.T) {
	fs := []health.Finding{
		mkFinding("a", "one", "R", health.SeverityInfo),
		mkFinding("b", "two", "R", health.SeverityError),
		mkFinding("c", "three", "R", health.SeverityWarn),
	}
	got := filter(fs, "", health.SeverityWarn)
	if len(got) != 2 {
		t.Fatalf("expected 2 findings at warn+, got %d", len(got))
	}
}

func TestFilterByTextMatchesSeveralFields(t *testing.T) {
	fs := []health.Finding{
		mkFinding("kube-system", "coredns", "CrashLoopBackOff", health.SeverityError),
		mkFinding("default", "web", "ImagePullBackOff", health.SeverityError),
	}
	for _, q := range []string{"kube-system", "coredns", "crashloop", "POD"} {
		if got := filter(fs, q, health.SeverityOk); len(got) == 0 {
			t.Errorf("query %q matched nothing", q)
		}
	}
	if got := filter(fs, "nonexistent", health.SeverityOk); len(got) != 0 {
		t.Errorf("expected no matches, got %d", len(got))
	}
}

func TestFilterIsCaseInsensitive(t *testing.T) {
	fs := []health.Finding{mkFinding("Default", "WebServer", "R", health.SeverityError)}
	if got := filter(fs, "webserver", health.SeverityOk); len(got) != 1 {
		t.Fatal("expected a case-insensitive match")
	}
}

func TestBarClampsAndFills(t *testing.T) {
	if got := bar(150, 10); !strings.Contains(got, "100%") {
		t.Errorf("expected over-100 input to clamp, got %q", got)
	}
	if got := bar(-5, 10); !strings.Contains(got, "  0%") {
		t.Errorf("expected negative input to clamp to 0, got %q", got)
	}
	// A full bar should carry no empty cells.
	if got := bar(100, 10); strings.Contains(got, "░") {
		t.Errorf("expected a full bar to have no empty cells, got %q", got)
	}
}

func TestSparklineHandlesFlatAndEmptySeries(t *testing.T) {
	if got := sparkline(nil, 10); got != "" {
		t.Errorf("expected empty output for no samples, got %q", got)
	}
	// An all-zero series must still render a baseline rather than blank.
	if got := sparkline([]float64{0, 0, 0}, 10); len([]rune(got)) != 3 {
		t.Errorf("expected 3 baseline runes, got %q", got)
	}
	if got := sparkline([]float64{1, 5, 9}, 10); len([]rune(got)) != 3 {
		t.Errorf("expected 3 runes, got %q", got)
	}
}

func TestSparklineTrimsToWidth(t *testing.T) {
	series := make([]float64, 100)
	for i := range series {
		series[i] = float64(i)
	}
	if got := sparkline(series, 20); len([]rune(got)) != 20 {
		t.Fatalf("expected 20 runes, got %d", len([]rune(got)))
	}
}

func TestAgeFormatsMagnitudes(t *testing.T) {
	now := time.Now()
	cases := []struct {
		at   time.Time
		want string
	}{
		{now.Add(-30 * time.Second), "30s"},
		{now.Add(-5 * time.Minute), "5m"},
		{now.Add(-3 * time.Hour), "3h"},
		{now.Add(-48 * time.Hour), "2d"},
		{time.Time{}, "-"},
	}
	for _, c := range cases {
		if got := age(c.at); got != c.want {
			t.Errorf("age(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}

func TestTruncateCollapsesNewlines(t *testing.T) {
	if got := truncate("a\nb", 10); got != "a b" {
		t.Errorf("expected newlines collapsed, got %q", got)
	}
	if got := truncate(strings.Repeat("x", 50), 10); len(got) != 10 {
		t.Errorf("expected length 10, got %d", len(got))
	}
}
