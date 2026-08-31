package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jthunderbird/kelper/internal/health"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Nodes reports node-level problems: failing conditions, cordoned nodes, and
// resource commitment. Node health was not checked at all previously.
type Nodes struct {
	// UtilizationThreshold is the percentage of allocatable at which usage is
	// flagged. Defaults to 85.
	UtilizationThreshold float64
}

// Name identifies the check.
func (Nodes) Name() string { return "nodes" }

// Description is one line of help text.
func (Nodes) Description() string {
	return "Node readiness, resource pressure, cordon state, overcommit and utilization"
}

// Requires reports no capabilities: the condition checks work everywhere and
// the metrics-backed portion degrades on its own.
func (Nodes) Requires() []health.Capability { return nil }

func (c Nodes) threshold() float64 {
	if c.UtilizationThreshold > 0 {
		return c.UtilizationThreshold
	}
	return 85
}

// Run evaluates node conditions, scheduling state and utilization.
func (c Nodes) Run(ctx context.Context, s *health.Snapshot) ([]health.Finding, error) {
	var out []health.Finding

	// Sum pod requests per node so overcommit can be reported without metrics.
	requested := map[string]*requestTotals{}
	for i := range s.Pods {
		p := &s.Pods[i]
		if p.Spec.NodeName == "" || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		t := requested[p.Spec.NodeName]
		if t == nil {
			t = &requestTotals{}
			requested[p.Spec.NodeName] = t
		}
		for _, ctr := range p.Spec.Containers {
			t.cpu += ctr.Resources.Requests.Cpu().MilliValue()
			t.mem += ctr.Resources.Requests.Memory().Value()
		}
	}

	usage := map[string]nodeUsage{}
	for _, nm := range s.NodeMetrics {
		usage[nm.Name] = nodeUsage{
			cpu: nm.Usage.Cpu().MilliValue(),
			mem: nm.Usage.Memory().Value(),
		}
	}

	for i := range s.Nodes {
		n := &s.Nodes[i]
		ref := health.ResourceRef{Kind: "Node", Name: n.Name, Node: n.Name}

		out = append(out, c.conditions(n, ref)...)

		if n.Spec.Unschedulable {
			out = append(out, health.Finding{
				Severity:    health.SeverityWarn,
				Ref:         ref,
				Reason:      "Cordoned",
				Message:     "node is cordoned and will not accept new pods",
				Remediation: "kubectl uncordon " + n.Name,
			})
		}

		alloc := n.Status.Allocatable
		if t := requested[n.Name]; t != nil {
			out = append(out, c.overcommit(n, ref, t, alloc)...)
		}
		if u, ok := usage[n.Name]; ok {
			out = append(out, c.utilization(ref, u, alloc)...)
		}
		if fsUsage, ok := s.NodeFilesystems[n.Name]; ok {
			out = append(out, c.filesystem(ref, fsUsage)...)
		}
	}
	return out, nil
}

type requestTotals struct{ cpu, mem int64 }
type nodeUsage struct{ cpu, mem int64 }

// conditions reports failing node conditions. Ready is inverted relative to the
// pressure conditions, so each is checked against the status that means trouble.
func (c Nodes) conditions(n *corev1.Node, ref health.ResourceRef) []health.Finding {
	var out []health.Finding
	for _, cond := range n.Status.Conditions {
		switch cond.Type {
		case corev1.NodeReady:
			if cond.Status != corev1.ConditionTrue {
				out = append(out, health.Finding{
					Severity: health.SeverityCritical,
					Ref:      ref,
					Reason:   "NodeNotReady",
					Message: fmt.Sprintf("node is not ready: %s",
						firstNonEmpty(cond.Message, cond.Reason)),
					Detail:      cond.Message,
					Remediation: "Check the kubelet and container runtime on this node",
					Since:       cond.LastTransitionTime.Time,
				})
			}
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure, corev1.NodeNetworkUnavailable:
			if cond.Status == corev1.ConditionTrue {
				out = append(out, health.Finding{
					Severity: health.SeverityError,
					Ref:      ref,
					Reason:   string(cond.Type),
					Message: fmt.Sprintf("%s: %s", cond.Type,
						firstNonEmpty(cond.Message, cond.Reason)),
					Detail:      cond.Message,
					Remediation: "Pods may be evicted from this node until the pressure clears",
					Since:       cond.LastTransitionTime.Time,
				})
			}
		}
	}
	return out
}

// overcommit compares scheduled pod requests against allocatable capacity.
func (c Nodes) overcommit(n *corev1.Node, ref health.ResourceRef, t *requestTotals, alloc corev1.ResourceList) []health.Finding {
	var out []health.Finding
	if cpu := alloc.Cpu(); cpu != nil && cpu.MilliValue() > 0 {
		if pct := float64(t.cpu) / float64(cpu.MilliValue()) * 100; pct >= c.threshold() {
			out = append(out, health.Finding{
				Severity: severityForPercent(pct),
				Ref:      ref,
				Reason:   "CPURequestsHigh",
				Message: fmt.Sprintf("pod CPU requests are %.0f%% of allocatable (%s of %s)",
					pct, milliCores(t.cpu), milliCores(cpu.MilliValue())),
				Remediation: "New pods may fail to schedule on this node",
			})
		}
	}
	if mem := alloc.Memory(); mem != nil && mem.Value() > 0 {
		if pct := float64(t.mem) / float64(mem.Value()) * 100; pct >= c.threshold() {
			out = append(out, health.Finding{
				Severity: severityForPercent(pct),
				Ref:      ref,
				Reason:   "MemoryRequestsHigh",
				Message: fmt.Sprintf("pod memory requests are %.0f%% of allocatable (%s of %s)",
					pct, humanBytes(uint64(t.mem)), humanBytes(uint64(mem.Value()))),
				Remediation: "New pods may fail to schedule on this node",
			})
		}
	}
	return out
}

// utilization compares live usage against allocatable.
func (c Nodes) utilization(ref health.ResourceRef, u nodeUsage, alloc corev1.ResourceList) []health.Finding {
	var out []health.Finding
	if cpu := alloc.Cpu(); cpu != nil && cpu.MilliValue() > 0 {
		if pct := float64(u.cpu) / float64(cpu.MilliValue()) * 100; pct >= c.threshold() {
			out = append(out, health.Finding{
				Severity: severityForPercent(pct),
				Ref:      ref,
				Reason:   "CPUUtilizationHigh",
				Message:  fmt.Sprintf("node CPU usage is %.0f%% of allocatable", pct),
				Evidence: []health.Evidence{{Label: "usage", Value: milliCores(u.cpu)}},
			})
		}
	}
	if mem := alloc.Memory(); mem != nil && mem.Value() > 0 {
		if pct := float64(u.mem) / float64(mem.Value()) * 100; pct >= c.threshold() {
			out = append(out, health.Finding{
				Severity: severityForPercent(pct),
				Ref:      ref,
				Reason:   "MemoryUtilizationHigh",
				Message:  fmt.Sprintf("node memory usage is %.0f%% of allocatable", pct),
				Evidence: []health.Evidence{{Label: "usage", Value: humanBytes(uint64(u.mem))}},
			})
		}
	}
	return out
}

// filesystem reports kubelet-reported disk usage, which fires before the
// DiskPressure condition does.
func (c Nodes) filesystem(ref health.ResourceRef, fs health.NodeFsStat) []health.Finding {
	var out []health.Finding
	if fs.CapacityBytes > 0 {
		if pct := float64(fs.UsedBytes) / float64(fs.CapacityBytes) * 100; pct >= c.threshold() {
			out = append(out, health.Finding{
				Severity: severityForPercent(pct),
				Ref:      ref,
				Reason:   "DiskUsageHigh",
				Message: fmt.Sprintf("node filesystem is %.0f%% full (%s of %s)",
					pct, humanBytes(fs.UsedBytes), humanBytes(fs.CapacityBytes)),
				Remediation: "Prune unused images and logs before the kubelet starts evicting pods",
			})
		}
	}
	return out
}

// severityForPercent escalates from warning to error as a resource fills.
func severityForPercent(pct float64) health.Severity {
	if pct >= 95 {
		return health.SeverityError
	}
	return health.SeverityWarn
}

func milliCores(m int64) string {
	return resource.NewMilliQuantity(m, resource.DecimalSI).String()
}

// humanBytes renders a byte count in binary units.
func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTP"[exp])
}

// ageOf is a small helper for evidence lines.
func ageOf(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return roundDur(time.Since(t)).String()
}
