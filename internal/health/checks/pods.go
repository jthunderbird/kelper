// Package checks holds kelper's individual health checks. Each is a pure
// function of the shared snapshot so it can be unit tested against fixtures.
package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jthunderbird/kelper/internal/health"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// stuckTerminatingGrace is how far past its deletion grace period a pod must be
// before it is reported as stuck.
const stuckTerminatingGrace = 5 * time.Minute

// Pods reports pods that are not healthy. It covers several states the previous
// implementation missed entirely, most importantly pods with no container
// statuses at all (Pending/unschedulable), which a ready-container scan reports
// as healthy.
type Pods struct{}

// Name identifies the check.
func (Pods) Name() string { return "pods" }

// Description is one line of help text.
func (Pods) Description() string {
	return "Pods that are pending, failed, evicted, OOMKilled, image-pull-failing or stuck terminating"
}

// Requires reports no optional capabilities.
func (Pods) Requires() []health.Capability { return nil }

// Run evaluates every pod in the snapshot.
func (c Pods) Run(ctx context.Context, s *health.Snapshot) ([]health.Finding, error) {
	var out []health.Finding
	for i := range s.Pods {
		out = append(out, c.evaluate(s, &s.Pods[i])...)
	}
	return out, nil
}

func (c Pods) evaluate(s *health.Snapshot, p *corev1.Pod) []health.Finding {
	ref := health.ResourceRef{Kind: "Pod", Namespace: p.Namespace, Name: p.Name, Node: p.Spec.NodeName}
	var out []health.Finding

	// Stuck terminating: deletion requested long ago but the pod persists.
	if p.DeletionTimestamp != nil {
		grace := stuckTerminatingGrace
		if p.DeletionGracePeriodSeconds != nil {
			grace += time.Duration(*p.DeletionGracePeriodSeconds) * time.Second
		}
		if age := time.Since(p.DeletionTimestamp.Time); age > grace {
			out = append(out, health.Finding{
				Severity:    health.SeverityError,
				Ref:         ref,
				Reason:      "StuckTerminating",
				Message:     fmt.Sprintf("terminating for %s, past its grace period", roundDur(age)),
				Remediation: "Check for a blocking finalizer, or force delete with --grace-period=0 --force",
				Evidence:    evidenceFromEvents(s, p.UID, 2),
				Since:       p.DeletionTimestamp.Time,
			})
		}
		// A pod on its way out should not also be reported as unschedulable.
		return out
	}

	switch p.Status.Phase {
	case corev1.PodSucceeded:
		// Completed pods are healthy by definition.
		return out

	case corev1.PodPending:
		// Pending pods commonly have zero container statuses, so the container
		// scan below cannot see them at all.
		if f, ok := c.pendingFinding(s, p, ref); ok {
			out = append(out, f)
		}

	case corev1.PodFailed:
		reason := p.Status.Reason
		if reason == "" {
			reason = "Failed"
		}
		sev := health.SeverityError
		msg := fmt.Sprintf("pod failed: %s", reason)
		if reason == "Evicted" {
			msg = fmt.Sprintf("evicted: %s", firstNonEmpty(p.Status.Message, "node resource pressure"))
		}
		out = append(out, health.Finding{
			Severity:    sev,
			Ref:         ref,
			Reason:      reason,
			Message:     msg,
			Remediation: "Inspect the node's resource pressure and the pod's requests",
			Evidence:    evidenceFromEvents(s, p.UID, 2),
		})
	}

	// Container-level problems, for both init and regular containers.
	out = append(out, c.containerFindings(s, p, ref, p.Status.InitContainerStatuses, true)...)
	out = append(out, c.containerFindings(s, p, ref, p.Status.ContainerStatuses, false)...)

	return out
}

// pendingFinding diagnoses a Pending pod, preferring the scheduler's own
// explanation from the PodScheduled condition.
func (c Pods) pendingFinding(s *health.Snapshot, p *corev1.Pod, ref health.ResourceRef) (health.Finding, bool) {
	for _, cond := range p.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
			sev := health.SeverityError
			if cond.Reason == corev1.PodReasonSchedulerError {
				sev = health.SeverityCritical
			}
			return health.Finding{
				Severity: sev,
				Ref:      ref,
				Reason:   firstNonEmpty(cond.Reason, "Unschedulable"),
				Message:  fmt.Sprintf("cannot be scheduled: %s", firstNonEmpty(cond.Message, cond.Reason)),
				Detail:   cond.Message,
				Remediation: "Check node capacity, taints/tolerations, node selectors and " +
					"affinity rules against this pod's requirements",
				Evidence: evidenceFromEvents(s, p.UID, 3),
				Since:    cond.LastTransitionTime.Time,
			}, true
		}
	}
	// Scheduled but still pending: containers are starting or images pulling.
	// The container scan reports the specific reason, so only flag a long wait.
	if age := time.Since(p.CreationTimestamp.Time); age > 10*time.Minute && len(p.Status.ContainerStatuses) == 0 {
		return health.Finding{
			Severity:    health.SeverityWarn,
			Ref:         ref,
			Reason:      "PendingTooLong",
			Message:     fmt.Sprintf("pending for %s with no container status reported", roundDur(age)),
			Remediation: "Check the kubelet on the assigned node and the pod's events",
			Evidence:    evidenceFromEvents(s, p.UID, 3),
			Since:       p.CreationTimestamp.Time,
		}, true
	}
	return health.Finding{}, false
}

// containerFindings reports waiting and previously-terminated container states.
func (c Pods) containerFindings(s *health.Snapshot, p *corev1.Pod, ref health.ResourceRef, statuses []corev1.ContainerStatus, init bool) []health.Finding {
	var out []health.Finding
	kind := "container"
	if init {
		kind = "init container"
	}
	for _, cs := range statuses {
		// An init container that has completed is expected to be not-ready.
		if init && cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0 {
			continue
		}

		if w := cs.State.Waiting; w != nil {
			if sev, ok := waitingSeverity(w.Reason); ok {
				out = append(out, health.Finding{
					Severity:    sev,
					Ref:         ref,
					Reason:      w.Reason,
					Message:     fmt.Sprintf("%s %q is %s", kind, cs.Name, w.Reason),
					Detail:      w.Message,
					Remediation: remediationFor(w.Reason),
					Evidence: append(
						[]health.Evidence{{Label: "container", Value: cs.Name}},
						evidenceFromEvents(s, p.UID, 2)...),
				})
			}
		}

		// OOMKilled is reported from the previous termination, so it is visible
		// even while the container is currently running.
		if t := cs.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
			out = append(out, health.Finding{
				Severity: health.SeverityError,
				Ref:      ref,
				Reason:   "OOMKilled",
				Message: fmt.Sprintf("%s %q was OOMKilled (restarts: %d)",
					kind, cs.Name, cs.RestartCount),
				Remediation: "Raise the memory limit, or profile the container for a leak",
				Evidence: []health.Evidence{
					{Label: "container", Value: cs.Name},
					{Label: "exit code", Value: fmt.Sprint(t.ExitCode)},
					{Label: "last OOM", Value: t.FinishedAt.Format(time.RFC3339), Time: t.FinishedAt.Time},
				},
				Since: t.FinishedAt.Time,
			})
		}
	}
	return out
}

// waitingSeverity maps a container waiting reason to a severity. Reasons that
// are part of normal startup return false and are not reported.
func waitingSeverity(reason string) (health.Severity, bool) {
	switch reason {
	case "CrashLoopBackOff":
		return health.SeverityError, true
	case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
		return health.SeverityError, true
	case "CreateContainerConfigError", "CreateContainerError", "RunContainerError":
		return health.SeverityError, true
	case "ContainerCreating", "PodInitializing", "":
		return health.SeverityOk, false
	default:
		return health.SeverityWarn, true
	}
}

func remediationFor(reason string) string {
	switch reason {
	case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
		return "Verify the image name/tag exists and that imagePullSecrets are correct"
	case "CrashLoopBackOff":
		return "Check container logs and the previous termination exit code"
	case "CreateContainerConfigError":
		return "A referenced ConfigMap or Secret key is probably missing"
	default:
		return ""
	}
}

// evidenceFromEvents attaches up to n recent warning events for an object, so
// a finding carries the cluster's own explanation of why it fired.
func evidenceFromEvents(s *health.Snapshot, uid types.UID, n int) []health.Evidence {
	evs := s.WarningEventsFor(uid)
	if len(evs) > n {
		evs = evs[:n]
	}
	out := make([]health.Evidence, 0, len(evs))
	for _, e := range evs {
		out = append(out, health.Evidence{
			Label: "event/" + e.Reason,
			Value: e.Message,
			Time:  eventTime(e),
		})
	}
	return out
}

// eventTime picks the freshest timestamp an event carries.
func eventTime(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.FirstTimestamp.Time
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// roundDur trims a duration to a readable magnitude.
func roundDur(d time.Duration) time.Duration {
	switch {
	case d > time.Hour:
		return d.Round(time.Minute)
	case d > time.Minute:
		return d.Round(time.Second)
	default:
		return d.Round(time.Second)
	}
}
