package checks

import (
	"context"
	"fmt"

	"github.com/jthunderbird/kelper/internal/health"
	corev1 "k8s.io/api/core/v1"
)

// Limits reports containers running close to or above their configured limits.
// Memory approaching its limit is the leading predictor of an OOMKill, and CPU
// at its limit means the container is being throttled — neither is visible from
// pod status alone.
type Limits struct {
	// Threshold is the percentage of a limit at which usage is flagged.
	// Defaults to 85.
	Threshold float64
	// ReportMissing enables an informational finding for containers with no
	// limits or requests set.
	ReportMissing bool
}

// Name identifies the check.
func (Limits) Name() string { return "limits" }

// Description is one line of help text.
func (Limits) Description() string {
	return "Containers near or over their CPU/memory limits, and containers with no limits set"
}

// Requires metrics-server for usage readings.
func (Limits) Requires() []health.Capability { return []health.Capability{health.CapMetrics} }

func (c Limits) threshold() float64 {
	if c.Threshold > 0 {
		return c.Threshold
	}
	return 85
}

// Run compares live container usage against configured limits.
func (c Limits) Run(ctx context.Context, s *health.Snapshot) ([]health.Finding, error) {
	// Index pod specs so each metrics sample can find its container's limits.
	type ctrKey struct{ ns, pod, ctr string }
	limits := map[ctrKey]corev1.ResourceRequirements{}
	nodeOf := map[string]string{}
	for i := range s.Pods {
		p := &s.Pods[i]
		nodeOf[p.Namespace+"/"+p.Name] = p.Spec.NodeName
		for _, ctr := range p.Spec.Containers {
			limits[ctrKey{p.Namespace, p.Name, ctr.Name}] = ctr.Resources
		}
	}

	var out []health.Finding
	for _, pm := range s.PodMetrics {
		for _, cm := range pm.Containers {
			req, ok := limits[ctrKey{pm.Namespace, pm.Name, cm.Name}]
			if !ok {
				continue
			}
			ref := health.ResourceRef{
				Kind: "Pod", Namespace: pm.Namespace, Name: pm.Name,
				Node: nodeOf[pm.Namespace+"/"+pm.Name],
			}

			if lim := req.Limits.Memory(); lim != nil && lim.Value() > 0 {
				used := cm.Usage.Memory().Value()
				if pct := float64(used) / float64(lim.Value()) * 100; pct >= c.threshold() {
					out = append(out, health.Finding{
						Severity: severityForPercent(pct),
						Ref:      ref,
						Reason:   "MemoryNearLimit",
						Message: fmt.Sprintf("container %q is at %.0f%% of its memory limit (%s of %s)",
							cm.Name, pct, humanBytes(uint64(used)), lim.String()),
						Detail:      "A container that reaches its memory limit is OOMKilled by the kernel.",
						Remediation: "Raise the memory limit or reduce the container's footprint",
						Evidence: []health.Evidence{
							{Label: "container", Value: cm.Name},
							{Label: "usage", Value: humanBytes(uint64(used))},
							{Label: "limit", Value: lim.String()},
						},
					})
				}
			}

			if lim := req.Limits.Cpu(); lim != nil && lim.MilliValue() > 0 {
				used := cm.Usage.Cpu().MilliValue()
				if pct := float64(used) / float64(lim.MilliValue()) * 100; pct >= c.threshold() {
					out = append(out, health.Finding{
						Severity: severityForPercent(pct),
						Ref:      ref,
						Reason:   "CPUNearLimit",
						Message: fmt.Sprintf("container %q is at %.0f%% of its CPU limit (%s of %s)",
							cm.Name, pct, milliCores(used), lim.String()),
						Detail:      "A container at its CPU limit is throttled, which shows up as latency rather than failure.",
						Remediation: "Raise the CPU limit or profile the workload",
						Evidence: []health.Evidence{
							{Label: "container", Value: cm.Name},
							{Label: "usage", Value: milliCores(used)},
							{Label: "limit", Value: lim.String()},
						},
					})
				}
			}
		}
	}

	if c.ReportMissing {
		out = append(out, c.missingLimits(s)...)
	}
	return out, nil
}

// missingLimits reports containers with no resource constraints. These are
// scheduled as best-effort and are the first evicted under node pressure.
func (c Limits) missingLimits(s *health.Snapshot) []health.Finding {
	var out []health.Finding
	for i := range s.Pods {
		p := &s.Pods[i]
		if p.Status.Phase == corev1.PodSucceeded || p.DeletionTimestamp != nil {
			continue
		}
		var bare []string
		for _, ctr := range p.Spec.Containers {
			if len(ctr.Resources.Limits) == 0 && len(ctr.Resources.Requests) == 0 {
				bare = append(bare, ctr.Name)
			}
		}
		if len(bare) == 0 {
			continue
		}
		out = append(out, health.Finding{
			Severity: health.SeverityInfo,
			Ref:      health.ResourceRef{Kind: "Pod", Namespace: p.Namespace, Name: p.Name, Node: p.Spec.NodeName},
			Reason:   "NoResourceLimits",
			Message: fmt.Sprintf("%d container(s) have no requests or limits: %v",
				len(bare), bare),
			Detail:      "Best-effort pods are the first to be evicted when a node comes under pressure.",
			Remediation: "Set resource requests and limits, or apply a LimitRange to the namespace",
		})
	}
	return out
}
