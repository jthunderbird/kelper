package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jthunderbird/kelper/internal/health"
	corev1 "k8s.io/api/core/v1"
)

// DefaultRestartWindow matches the kubelet's CrashLoopBackOff decay: a
// container that stays up this long has its backoff timer reset. A pod that
// restarted more recently is therefore still inside its restart loop and is
// likely to restart again, even though it is Running and Ready right now.
const DefaultRestartWindow = 10 * time.Minute

// Restarts reports pods that restarted recently enough to still be inside the
// kubelet's backoff window. These are invisible to a plain readiness check:
// between crashes the pod looks perfectly healthy.
type Restarts struct {
	// Window overrides DefaultRestartWindow when non-zero.
	Window time.Duration
	// SustainedRestarts is the restart count at which churn is treated as an
	// error rather than a warning.
	SustainedRestarts int32
}

// Name identifies the check.
func (Restarts) Name() string { return "restarts" }

// Description is one line of help text.
func (Restarts) Description() string {
	return "Pods that restarted inside the kubelet backoff window and are likely to restart again"
}

// Requires reports no optional capabilities.
func (Restarts) Requires() []health.Capability { return nil }

func (c Restarts) window() time.Duration {
	if c.Window > 0 {
		return c.Window
	}
	return DefaultRestartWindow
}

func (c Restarts) sustained() int32 {
	if c.SustainedRestarts > 0 {
		return c.SustainedRestarts
	}
	return 5
}

// Run evaluates restart recency for every container in the snapshot.
func (c Restarts) Run(ctx context.Context, s *health.Snapshot) ([]health.Finding, error) {
	var out []health.Finding
	for i := range s.Pods {
		p := &s.Pods[i]
		if p.Status.Phase == corev1.PodSucceeded || p.DeletionTimestamp != nil {
			continue
		}
		ref := health.ResourceRef{Kind: "Pod", Namespace: p.Namespace, Name: p.Name, Node: p.Spec.NodeName}
		for _, cs := range p.Status.ContainerStatuses {
			if f, ok := c.evaluate(p, cs, ref); ok {
				out = append(out, f)
			}
		}
	}
	return out, nil
}

func (c Restarts) evaluate(p *corev1.Pod, cs corev1.ContainerStatus, ref health.ResourceRef) (health.Finding, bool) {
	if cs.RestartCount == 0 {
		return health.Finding{}, false
	}
	term := cs.LastTerminationState.Terminated
	if term == nil || term.FinishedAt.IsZero() {
		return health.Finding{}, false
	}

	since := time.Since(term.FinishedAt.Time)
	if since > c.window() {
		// Restarts happened, but the container has since been stable long
		// enough for the kubelet to reset its backoff. Not currently at risk.
		return health.Finding{}, false
	}

	// A container already back in CrashLoopBackOff is reported by the pods
	// check; this check exists for the ones that look fine right now.
	sev := health.SeverityWarn
	if cs.RestartCount >= c.sustained() {
		sev = health.SeverityError
	}

	state := "running"
	if cs.State.Waiting != nil {
		state = cs.State.Waiting.Reason
	} else if cs.State.Terminated != nil {
		state = "terminated"
	}

	return health.Finding{
		Severity: sev,
		Ref:      ref,
		Reason:   "RestartChurn",
		Message: fmt.Sprintf("container %q restarted %d time(s), last %s ago — still inside the %s backoff window",
			cs.Name, cs.RestartCount, roundDur(since), c.window()),
		Detail: fmt.Sprintf("Currently %s. The kubelet resets its restart backoff after %s of uptime, "+
			"so this container is likely to restart again.", state, c.window()),
		Remediation: "Check logs for the previous run: kubectl logs --previous",
		Evidence: []health.Evidence{
			{Label: "container", Value: cs.Name},
			{Label: "restarts", Value: fmt.Sprint(cs.RestartCount)},
			{Label: "last exit code", Value: fmt.Sprint(term.ExitCode)},
			{Label: "last reason", Value: firstNonEmpty(term.Reason, "unknown")},
			{Label: "last restart", Value: term.FinishedAt.Format(time.RFC3339), Time: term.FinishedAt.Time},
		},
		Since: term.FinishedAt.Time,
	}, true
}
