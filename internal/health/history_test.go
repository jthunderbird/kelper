package health_test

import (
	"testing"

	"github.com/jthunderbird/kelper/internal/health"
)

func finding(name, reason string) health.Finding {
	return health.Finding{
		Check:    "test",
		Severity: health.SeverityError,
		Ref:      health.ResourceRef{Kind: "Pod", Namespace: "default", Name: name},
		Reason:   reason,
	}
}

func result(fs ...health.Finding) health.Result {
	return health.Result{Findings: fs}
}

func countDelta(changes []health.Change, d health.Delta) int {
	n := 0
	for _, c := range changes {
		if c.Delta == d {
			n++
		}
	}
	return n
}

func TestFirstRunReportsEverythingAsNew(t *testing.T) {
	h := health.NewHistory()
	changes := h.Observe(result(finding("a", "R1"), finding("b", "R2")))
	if got := countDelta(changes, health.DeltaNew); got != 2 {
		t.Fatalf("expected 2 new, got %d", got)
	}
}

func TestUnchangedFindingIsPersistent(t *testing.T) {
	h := health.NewHistory()
	h.Observe(result(finding("a", "R1")))
	changes := h.Observe(result(finding("a", "R1")))
	if got := countDelta(changes, health.DeltaPersistent); got != 1 {
		t.Fatalf("expected 1 persistent, got %d: %+v", got, changes)
	}
	if countDelta(changes, health.DeltaNew) != 0 {
		t.Error("expected no new findings on an unchanged run")
	}
}

func TestDisappearedFindingIsResolved(t *testing.T) {
	h := health.NewHistory()
	h.Observe(result(finding("a", "R1"), finding("b", "R2")))
	changes := h.Observe(result(finding("a", "R1")))
	if got := countDelta(changes, health.DeltaResolved); got != 1 {
		t.Fatalf("expected 1 resolved, got %d", got)
	}
}

// A finding that keeps flipping matters more than any single occurrence.
func TestRepeatedFlipsMarkFlapping(t *testing.T) {
	h := health.NewHistory()
	f := finding("flappy", "R1")
	for range 3 {
		h.Observe(result(f))
		h.Observe(result())
	}
	changes := h.Observe(result(f))
	for _, c := range changes {
		if c.Finding.Ref.Name == "flappy" {
			if !c.Flapping {
				t.Fatal("expected the finding to be marked flapping")
			}
			return
		}
	}
	t.Fatal("flapping finding not present in changes")
}

// Age must survive across runs so the dashboard can show how long a problem
// has persisted.
func TestSinceIsStableAcrossRuns(t *testing.T) {
	h := health.NewHistory()
	h.Observe(result(finding("a", "R1")))
	first := h.Current()[0].Since

	h.Observe(result(finding("a", "R1")))
	if got := h.Current()[0].Since; !got.Equal(first) {
		t.Fatalf("expected Since to be stable, was %v then %v", first, got)
	}
}

func TestSeriesTracksSeverityOverTime(t *testing.T) {
	h := health.NewHistory()
	h.Observe(result(finding("a", "R1")))
	h.Observe(result(finding("a", "R1"), finding("b", "R2")))
	series := h.Series(health.SeverityError)
	if len(series) != 2 || series[0] != 1 || series[1] != 2 {
		t.Fatalf("expected [1 2], got %v", series)
	}
}
