package health

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Capability is an optional cluster data source a check may depend on. Checks
// declaring a capability that the cluster does not offer are skipped rather
// than failed, and reported in the run's Degraded list.
type Capability string

const (
	// CapMetrics is the metrics.k8s.io aggregated API (metrics-server).
	CapMetrics Capability = "metrics"
	// CapNodeProxy is permission to reach nodes/proxy, used for the kubelet
	// summary and metrics endpoints.
	CapNodeProxy Capability = "node-proxy"
	// CapDynamic is discovery plus dynamic listing, used to scan custom
	// resources.
	CapDynamic Capability = "dynamic"
)

// Check is a single diagnosis rule. Implementations must be pure functions of
// the snapshot: no API calls, so they can run against a fake clientset and
// under either the list-based or informer-based snapshot provider.
type Check interface {
	// Name is the stable identifier used by --checks and --skip.
	Name() string
	// Description is one line of help text.
	Description() string
	// Requires lists capabilities the check cannot run without.
	Requires() []Capability
	// Run evaluates the snapshot and returns any findings.
	Run(ctx context.Context, s *Snapshot) ([]Finding, error)
}

// Registry holds the set of available checks.
type Registry struct {
	mu     sync.RWMutex
	checks map[string]Check
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{checks: map[string]Check{}}
}

// Register adds a check. It panics on duplicate names, which can only be a
// programming error at wiring time.
func (r *Registry) Register(c Check) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.checks[c.Name()]; dup {
		panic(fmt.Sprintf("health: duplicate check %q", c.Name()))
	}
	r.checks[c.Name()] = c
}

// All returns every registered check, ordered by name.
func (r *Registry) All() []Check {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Check, 0, len(r.checks))
	for _, c := range r.checks {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Select resolves the checks to run. An empty include list means all checks;
// names in exclude are dropped. Unknown names in either list are an error, so a
// typo in --checks fails loudly instead of silently running nothing.
func (r *Registry) Select(include, exclude []string) ([]Check, error) {
	all := r.All()
	known := map[string]Check{}
	for _, c := range all {
		known[c.Name()] = c
	}
	for _, n := range append(append([]string{}, include...), exclude...) {
		if _, ok := known[n]; !ok {
			return nil, fmt.Errorf("unknown check %q (available: %s)", n, strings.Join(names(all), ", "))
		}
	}
	excluded := map[string]bool{}
	for _, n := range exclude {
		excluded[n] = true
	}
	var out []Check
	for _, c := range all {
		if excluded[c.Name()] {
			continue
		}
		if len(include) > 0 && !contains(include, c.Name()) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func names(cs []Check) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name()
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// Result is the outcome of running a set of checks.
type Result struct {
	Findings []Finding
	// Skipped maps a check name to the reason it did not run.
	Skipped map[string]string
	// Degraded lists human-readable capability gaps, e.g. "metrics-server
	// unavailable". Renderers surface these as a single status line.
	Degraded []string
}

// MaxSeverity returns the highest severity in the result, false when empty.
func (r Result) MaxSeverity() (Severity, bool) { return Tally(r.Findings).Max() }

// defaultConcurrency bounds how many checks evaluate at once.
const defaultConcurrency = 8

// Run evaluates checks against the snapshot concurrently. A check returning an
// error does not abort the run: the error is converted into an Error-severity
// finding attributed to the check, so one broken check cannot blind the others.
func Run(ctx context.Context, checks []Check, s *Snapshot) Result {
	res := Result{Skipped: map[string]string{}}

	var runnable []Check
	for _, c := range checks {
		if missing, ok := s.Missing(c.Requires()); !ok {
			res.Skipped[c.Name()] = fmt.Sprintf("requires %s", strings.Join(missing, ", "))
			continue
		}
		runnable = append(runnable, c)
	}
	res.Degraded = s.Degraded()

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, defaultConcurrency)
	)
	for _, c := range runnable {
		wg.Add(1)
		go func(c Check) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			fs, err := c.Run(ctx, s)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.Findings = append(res.Findings, Finding{
					Check:    c.Name(),
					Severity: SeverityError,
					Ref:      ResourceRef{Kind: "Check", Name: c.Name()},
					Reason:   "CheckFailed",
					Message:  fmt.Sprintf("check %q failed: %s", c.Name(), err),
				})
				return
			}
			for i := range fs {
				if fs[i].Check == "" {
					fs[i].Check = c.Name()
				}
			}
			res.Findings = append(res.Findings, fs...)
		}(c)
	}
	wg.Wait()

	SortFindings(res.Findings)
	return res
}
