// Package health implements kelper's cluster diagnosis engine: a registry of
// independent checks that run against a shared cluster snapshot and emit
// severity-ranked findings for the CLI, dashboard and watch renderers.
package health

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Severity ranks how urgently a finding needs attention.
type Severity int

const (
	// SeverityOk is a passing observation. Checks rarely emit these; they exist
	// so the dashboard can show green state for a resource it is tracking.
	SeverityOk Severity = iota
	// SeverityInfo is a notable but harmless observation.
	SeverityInfo
	// SeverityWarn is the "at risk" tier: not broken yet, but trending that way
	// (approaching a limit, restarted recently, cordoned).
	SeverityWarn
	// SeverityError is a resource that is currently not working.
	SeverityError
	// SeverityCritical is a failure with cluster-wide blast radius.
	SeverityCritical
)

var severityNames = map[Severity]string{
	SeverityOk:       "OK",
	SeverityInfo:     "INFO",
	SeverityWarn:     "WARN",
	SeverityError:    "ERROR",
	SeverityCritical: "CRITICAL",
}

// String returns the uppercase label for a severity.
func (s Severity) String() string {
	if n, ok := severityNames[s]; ok {
		return n
	}
	return "UNKNOWN"
}

// ParseSeverity resolves a severity by name, case-insensitively.
func ParseSeverity(s string) (Severity, error) {
	for sev, name := range severityNames {
		if strings.EqualFold(name, s) {
			return sev, nil
		}
	}
	return SeverityOk, fmt.Errorf("unknown severity %q", s)
}

// ResourceRef identifies the Kubernetes object a finding is about. Namespace is
// empty for cluster-scoped resources.
type ResourceRef struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
	// Node is the node hosting the resource, when relevant (pods, kubelet
	// certs, volume stats). Empty otherwise.
	Node string
}

// String renders the ref as kind/name, namespace-qualified when namespaced.
func (r ResourceRef) String() string {
	kind := strings.ToLower(r.Kind)
	if r.Namespace == "" {
		return fmt.Sprintf("%s/%s", kind, r.Name)
	}
	return fmt.Sprintf("%s/%s -n %s", kind, r.Name, r.Namespace)
}

// Evidence is a supporting datum explaining why a finding fired: a correlated
// event, a metric reading, a container exit code.
type Evidence struct {
	Label string
	Value string
	// Time is when the evidence was observed. Zero when not time-bound.
	Time time.Time
}

// Finding is a single diagnosis emitted by a check.
type Finding struct {
	// Check is the registered name of the emitting check.
	Check    string
	Severity Severity
	Ref      ResourceRef
	// Reason is a short stable code (e.g. "CrashLoopRisk") suitable for
	// grouping and filtering.
	Reason string
	// Message is a one-line human summary.
	Message string
	// Detail is optional extra context shown in expanded output.
	Detail      string
	Evidence    []Evidence
	Remediation string
	// Since is when the underlying condition was first observed. The watch and
	// dashboard modes carry this across runs; one-shot runs leave it zero.
	Since time.Time
}

// Key is the stable identity of a finding across runs. The dashboard uses it to
// keep table selection steady, and the watch differ uses it to classify
// findings as new, resolved or persistent.
func (f Finding) Key() string {
	return strings.Join([]string{
		f.Check, f.Ref.Group, f.Ref.Kind, f.Ref.Namespace, f.Ref.Name, f.Reason,
	}, "|")
}

// SortFindings orders findings most severe first, then by namespace, kind and
// name so output is deterministic across runs.
func SortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		if a.Ref.Namespace != b.Ref.Namespace {
			return a.Ref.Namespace < b.Ref.Namespace
		}
		if a.Ref.Kind != b.Ref.Kind {
			return a.Ref.Kind < b.Ref.Kind
		}
		return a.Ref.Name < b.Ref.Name
	})
}

// Counts tallies findings by severity.
type Counts map[Severity]int

// Tally counts findings by severity.
func Tally(fs []Finding) Counts {
	c := Counts{}
	for _, f := range fs {
		c[f.Severity]++
	}
	return c
}

// Max returns the highest severity present, and false when there are none.
func (c Counts) Max() (Severity, bool) {
	found := false
	max := SeverityOk
	for sev, n := range c {
		if n > 0 && (!found || sev > max) {
			max, found = sev, true
		}
	}
	return max, found
}
