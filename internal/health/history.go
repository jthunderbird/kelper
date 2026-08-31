package health

import (
	"sort"
	"sync"
	"time"
)

// defaultHistoryDepth is how many runs the ring buffer keeps for sparklines.
const defaultHistoryDepth = 120

// flapThreshold is how many appear/disappear transitions within the retained
// history mark a finding as flapping.
const flapThreshold = 3

// Delta classifies how a finding changed between two runs.
type Delta int

const (
	// DeltaNew is a finding that was not present in the previous run.
	DeltaNew Delta = iota
	// DeltaResolved is a finding that was present and no longer is.
	DeltaResolved
	// DeltaPersistent is a finding present in both runs.
	DeltaPersistent
)

// String names the delta.
func (d Delta) String() string {
	switch d {
	case DeltaNew:
		return "new"
	case DeltaResolved:
		return "resolved"
	default:
		return "persistent"
	}
}

// Change pairs a finding with how it changed.
type Change struct {
	Delta   Delta
	Finding Finding
	// Flapping is true when this finding has appeared and disappeared
	// repeatedly, which usually matters more than any single occurrence.
	Flapping bool
}

// Sample is one run's tallies, retained for trend rendering.
type Sample struct {
	At     time.Time
	Counts Counts
	Total  int
}

// History tracks findings across runs so the watch and dashboard modes can
// report what changed rather than repeating the same list.
type History struct {
	mu sync.Mutex
	// depth bounds the retained samples.
	depth int
	// current maps finding key to the finding as last seen.
	current map[string]Finding
	// firstSeen remembers when each finding first appeared, so a finding's age
	// survives across runs.
	firstSeen map[string]time.Time
	// transitions counts appear/disappear flips per key, for flap detection.
	transitions map[string]int
	samples     []Sample
	started     bool
}

// NewHistory returns an empty history.
func NewHistory() *History {
	return &History{
		depth:       defaultHistoryDepth,
		current:     map[string]Finding{},
		firstSeen:   map[string]time.Time{},
		transitions: map[string]int{},
	}
}

// Observe records a run and returns what changed relative to the previous one.
// The first run reports every finding as new.
func (h *History) Observe(res Result) []Change {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	next := make(map[string]Finding, len(res.Findings))
	for _, f := range res.Findings {
		key := f.Key()
		if first, ok := h.firstSeen[key]; ok {
			f.Since = first
		} else {
			h.firstSeen[key] = now
			if f.Since.IsZero() {
				f.Since = now
			}
		}
		next[key] = f
	}

	var changes []Change
	for key, f := range next {
		delta := DeltaPersistent
		if _, existed := h.current[key]; !existed {
			delta = DeltaNew
			if h.started {
				h.transitions[key]++
			}
		}
		changes = append(changes, Change{
			Delta:    delta,
			Finding:  f,
			Flapping: h.transitions[key] >= flapThreshold,
		})
	}
	for key, f := range h.current {
		if _, still := next[key]; still {
			continue
		}
		h.transitions[key]++
		changes = append(changes, Change{
			Delta:    DeltaResolved,
			Finding:  f,
			Flapping: h.transitions[key] >= flapThreshold,
		})
		// A resolved finding stops aging; if it returns it is a fresh
		// occurrence, but its flap count is deliberately retained.
		delete(h.firstSeen, key)
	}

	// Map iteration is unordered, so sort for stable output across runs:
	// resolved first (they are leaving), then by severity.
	sort.SliceStable(changes, func(i, j int) bool {
		a, b := changes[i], changes[j]
		if a.Delta != b.Delta {
			return a.Delta == DeltaResolved
		}
		if a.Finding.Severity != b.Finding.Severity {
			return a.Finding.Severity > b.Finding.Severity
		}
		if a.Finding.Ref.Namespace != b.Finding.Ref.Namespace {
			return a.Finding.Ref.Namespace < b.Finding.Ref.Namespace
		}
		return a.Finding.Key() < b.Finding.Key()
	})

	h.current = next
	h.started = true

	h.samples = append(h.samples, Sample{
		At: now, Counts: Tally(res.Findings), Total: len(res.Findings),
	})
	if len(h.samples) > h.depth {
		h.samples = h.samples[len(h.samples)-h.depth:]
	}

	return changes
}

// Samples returns the retained trend samples, oldest first.
func (h *History) Samples() []Sample {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Sample, len(h.samples))
	copy(out, h.samples)
	return out
}

// Series returns the per-run totals for a severity, for sparkline rendering.
func (h *History) Series(sev Severity) []float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]float64, len(h.samples))
	for i, s := range h.samples {
		out[i] = float64(s.Counts[sev])
	}
	return out
}

// Current returns the findings as of the last observed run.
func (h *History) Current() []Finding {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Finding, 0, len(h.current))
	for _, f := range h.current {
		out = append(out, f)
	}
	SortFindings(out)
	return out
}

// FirstSeen returns when a finding key was first observed.
func (h *History) FirstSeen(key string) (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.firstSeen[key]
	return t, ok
}
