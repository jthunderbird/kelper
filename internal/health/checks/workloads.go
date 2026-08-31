package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jthunderbird/kelper/internal/health"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// DefaultJobWindow bounds how far back a failed Job is still reported. Without
// it, a Job that failed once months ago is flagged forever.
const DefaultJobWindow = 24 * time.Hour

// Workloads reports controllers that are not meeting their desired state. It
// checks status conditions and rollout progress in addition to replica counts,
// so a stalled rollout at full replica count is no longer reported as healthy.
type Workloads struct {
	// JobWindow overrides DefaultJobWindow when non-zero.
	JobWindow time.Duration
}

// Name identifies the check.
func (Workloads) Name() string { return "workloads" }

// Description is one line of help text.
func (Workloads) Description() string {
	return "Deployments, StatefulSets, DaemonSets, Jobs and CronJobs not meeting desired state"
}

// Requires reports no optional capabilities.
func (Workloads) Requires() []health.Capability { return nil }

func (c Workloads) jobWindow() time.Duration {
	if c.JobWindow > 0 {
		return c.JobWindow
	}
	return DefaultJobWindow
}

// Run evaluates every workload kind in the snapshot.
func (c Workloads) Run(ctx context.Context, s *health.Snapshot) ([]health.Finding, error) {
	var out []health.Finding
	out = append(out, c.deployments(s)...)
	out = append(out, c.statefulSets(s)...)
	out = append(out, c.daemonSets(s)...)
	out = append(out, c.jobs(s)...)
	out = append(out, c.cronJobs(s)...)
	return out, nil
}

func (c Workloads) deployments(s *health.Snapshot) []health.Finding {
	var out []health.Finding
	for i := range s.Deployments {
		d := &s.Deployments[i]
		ref := health.ResourceRef{Group: "apps", Kind: "Deployment", Namespace: d.Namespace, Name: d.Name}

		// A spec change the controller has not yet observed means the rollout
		// has not even started.
		if d.Status.ObservedGeneration < d.Generation {
			out = append(out, health.Finding{
				Severity:    health.SeverityWarn,
				Ref:         ref,
				Reason:      "RolloutNotObserved",
				Message:     fmt.Sprintf("spec generation %d not yet observed by the controller (at %d)", d.Generation, d.Status.ObservedGeneration),
				Remediation: "Check the deployment controller in kube-controller-manager",
			})
		}

		for _, cond := range d.Status.Conditions {
			switch {
			case cond.Type == appsv1.DeploymentProgressing && cond.Status == corev1.ConditionFalse:
				out = append(out, health.Finding{
					Severity:    health.SeverityError,
					Ref:         ref,
					Reason:      firstNonEmpty(cond.Reason, "ProgressDeadlineExceeded"),
					Message:     fmt.Sprintf("rollout stalled: %s", firstNonEmpty(cond.Message, cond.Reason)),
					Detail:      cond.Message,
					Remediation: "Inspect the newest ReplicaSet's pods for the blocking failure",
					Since:       cond.LastTransitionTime.Time,
				})
			case cond.Type == appsv1.DeploymentReplicaFailure && cond.Status == corev1.ConditionTrue:
				out = append(out, health.Finding{
					Severity:    health.SeverityError,
					Ref:         ref,
					Reason:      "ReplicaFailure",
					Message:     fmt.Sprintf("cannot create pods: %s", firstNonEmpty(cond.Message, cond.Reason)),
					Detail:      cond.Message,
					Remediation: "Usually quota, PSA/PSP admission, or a missing service account",
					Since:       cond.LastTransitionTime.Time,
				})
			case cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionFalse:
				out = append(out, health.Finding{
					Severity: health.SeverityError,
					Ref:      ref,
					Reason:   "NotAvailable",
					Message:  fmt.Sprintf("no minimum availability: %s", firstNonEmpty(cond.Message, cond.Reason)),
					Detail:   cond.Message,
					Since:    cond.LastTransitionTime.Time,
				})
			}
		}

		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		if desired == 0 {
			continue
		}
		if avail := d.Status.AvailableReplicas; avail < desired {
			out = append(out, health.Finding{
				Severity:    severityForShortfall(avail, desired),
				Ref:         ref,
				Reason:      "ReplicasUnavailable",
				Message:     fmt.Sprintf("%d/%d replicas available", avail, desired),
				Remediation: "Inspect the pods owned by this deployment",
			})
		}
	}
	return out
}

func (c Workloads) statefulSets(s *health.Snapshot) []health.Finding {
	var out []health.Finding
	for i := range s.StatefulSets {
		st := &s.StatefulSets[i]
		ref := health.ResourceRef{Group: "apps", Kind: "StatefulSet", Namespace: st.Namespace, Name: st.Name}

		if st.Status.ObservedGeneration < st.Generation {
			out = append(out, health.Finding{
				Severity: health.SeverityWarn,
				Ref:      ref,
				Reason:   "RolloutNotObserved",
				Message:  fmt.Sprintf("spec generation %d not yet observed (at %d)", st.Generation, st.Status.ObservedGeneration),
			})
		}

		// A partially rolled-out StatefulSet keeps two revisions live. Ordered
		// rollouts stall silently on a failing pod, so this is the signal that
		// replica counts alone will not surface.
		if st.Status.CurrentRevision != "" && st.Status.UpdateRevision != "" &&
			st.Status.CurrentRevision != st.Status.UpdateRevision {
			out = append(out, health.Finding{
				Severity: health.SeverityWarn,
				Ref:      ref,
				Reason:   "RolloutIncomplete",
				Message: fmt.Sprintf("update in progress: %d/%d pods on the new revision",
					st.Status.UpdatedReplicas, specReplicas(st.Spec.Replicas)),
				Detail:      fmt.Sprintf("current=%s update=%s", st.Status.CurrentRevision, st.Status.UpdateRevision),
				Remediation: "A stuck ordered rollout is usually blocked on the lowest-ordinal unready pod",
			})
		}

		desired := specReplicas(st.Spec.Replicas)
		if desired == 0 {
			continue
		}
		if ready := st.Status.ReadyReplicas; ready < desired {
			out = append(out, health.Finding{
				Severity: severityForShortfall(ready, desired),
				Ref:      ref,
				Reason:   "ReplicasNotReady",
				Message:  fmt.Sprintf("%d/%d replicas ready", ready, desired),
			})
		}
	}
	return out
}

func (c Workloads) daemonSets(s *health.Snapshot) []health.Finding {
	var out []health.Finding
	for i := range s.DaemonSets {
		d := &s.DaemonSets[i]
		ref := health.ResourceRef{Group: "apps", Kind: "DaemonSet", Namespace: d.Namespace, Name: d.Name}
		st := d.Status

		if st.ObservedGeneration < d.Generation {
			out = append(out, health.Finding{
				Severity: health.SeverityWarn,
				Ref:      ref,
				Reason:   "RolloutNotObserved",
				Message:  fmt.Sprintf("spec generation %d not yet observed (at %d)", d.Generation, st.ObservedGeneration),
			})
		}
		if st.DesiredNumberScheduled > 0 && st.NumberReady < st.DesiredNumberScheduled {
			out = append(out, health.Finding{
				Severity:    severityForShortfall(st.NumberReady, st.DesiredNumberScheduled),
				Ref:         ref,
				Reason:      "PodsNotReady",
				Message:     fmt.Sprintf("%d/%d pods ready", st.NumberReady, st.DesiredNumberScheduled),
				Remediation: "Check the nodes where this DaemonSet's pods are not ready",
			})
		}
		if st.NumberMisscheduled > 0 {
			out = append(out, health.Finding{
				Severity:    health.SeverityWarn,
				Ref:         ref,
				Reason:      "Misscheduled",
				Message:     fmt.Sprintf("%d pod(s) running on nodes they should not be on", st.NumberMisscheduled),
				Remediation: "Node selectors or taints changed after the pods were placed",
			})
		}
		// Mid-rollout DaemonSets legitimately show unavailable pods; only flag
		// when the rollout is not the explanation.
		if st.NumberUnavailable > 0 && st.UpdatedNumberScheduled == st.DesiredNumberScheduled {
			out = append(out, health.Finding{
				Severity: health.SeverityWarn,
				Ref:      ref,
				Reason:   "PodsUnavailable",
				Message:  fmt.Sprintf("%d pod(s) unavailable after rollout completed", st.NumberUnavailable),
			})
		}
	}
	return out
}

func (c Workloads) jobs(s *health.Snapshot) []health.Finding {
	var out []health.Finding
	for i := range s.Jobs {
		j := &s.Jobs[i]
		ref := health.ResourceRef{Group: "batch", Kind: "Job", Namespace: j.Namespace, Name: j.Name}

		var failed, complete bool
		var failedAt time.Time
		var reason, message string
		for _, cond := range j.Status.Conditions {
			if cond.Status != corev1.ConditionTrue {
				continue
			}
			switch cond.Type {
			case batchv1.JobFailed:
				failed, failedAt = true, cond.LastTransitionTime.Time
				reason, message = cond.Reason, cond.Message
			case batchv1.JobComplete:
				complete = true
			}
		}

		if failed {
			// Only report recent failures; an ancient failed Job is history,
			// not an active problem.
			if age := time.Since(failedAt); age <= c.jobWindow() {
				out = append(out, health.Finding{
					Severity:    health.SeverityError,
					Ref:         ref,
					Reason:      firstNonEmpty(reason, "JobFailed"),
					Message:     fmt.Sprintf("job failed %s ago: %s", roundDur(age), firstNonEmpty(message, reason)),
					Detail:      message,
					Remediation: "Inspect the failed pods: kubectl logs job/" + j.Name,
					Since:       failedAt,
				})
			}
			continue
		}
		if complete {
			continue
		}

		// Still running: warn when it is heading for failure.
		if j.Spec.BackoffLimit != nil && *j.Spec.BackoffLimit > 0 && j.Status.Failed > 0 {
			if j.Status.Failed >= *j.Spec.BackoffLimit {
				out = append(out, health.Finding{
					Severity: health.SeverityError,
					Ref:      ref,
					Reason:   "BackoffLimitReached",
					Message:  fmt.Sprintf("%d/%d retries used", j.Status.Failed, *j.Spec.BackoffLimit),
				})
			} else if float64(j.Status.Failed) >= float64(*j.Spec.BackoffLimit)*0.7 {
				out = append(out, health.Finding{
					Severity: health.SeverityWarn,
					Ref:      ref,
					Reason:   "BackoffLimitNear",
					Message:  fmt.Sprintf("%d/%d retries used", j.Status.Failed, *j.Spec.BackoffLimit),
				})
			}
		}
		if j.Spec.ActiveDeadlineSeconds != nil && j.Status.StartTime != nil {
			deadline := time.Duration(*j.Spec.ActiveDeadlineSeconds) * time.Second
			if elapsed := time.Since(j.Status.StartTime.Time); elapsed > deadline {
				out = append(out, health.Finding{
					Severity: health.SeverityError,
					Ref:      ref,
					Reason:   "DeadlineExceeded",
					Message:  fmt.Sprintf("running %s, past its %s activeDeadlineSeconds", roundDur(elapsed), deadline),
				})
			}
		}
	}
	return out
}

func (c Workloads) cronJobs(s *health.Snapshot) []health.Finding {
	var out []health.Finding
	for i := range s.CronJobs {
		cj := &s.CronJobs[i]
		ref := health.ResourceRef{Group: "batch", Kind: "CronJob", Namespace: cj.Namespace, Name: cj.Name}

		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			out = append(out, health.Finding{
				Severity: health.SeverityInfo,
				Ref:      ref,
				Reason:   "Suspended",
				Message:  "cronjob is suspended and will not run",
			})
			continue
		}

		// A schedule that has never produced a job, long after creation, is
		// usually an invalid schedule or a disabled controller.
		if cj.Status.LastScheduleTime == nil {
			if age := time.Since(cj.CreationTimestamp.Time); age > 24*time.Hour {
				out = append(out, health.Finding{
					Severity:    health.SeverityWarn,
					Ref:         ref,
					Reason:      "NeverScheduled",
					Message:     fmt.Sprintf("created %s ago but has never scheduled a job", roundDur(age)),
					Detail:      "schedule: " + cj.Spec.Schedule,
					Remediation: "Verify the cron schedule expression is valid",
				})
			}
			continue
		}

		// Scheduled at some point, but nothing succeeded since.
		if cj.Status.LastSuccessfulTime != nil {
			gap := cj.Status.LastScheduleTime.Time.Sub(cj.Status.LastSuccessfulTime.Time)
			if gap > 24*time.Hour {
				out = append(out, health.Finding{
					Severity: health.SeverityWarn,
					Ref:      ref,
					Reason:   "NoRecentSuccess",
					Message: fmt.Sprintf("last success was %s before the last schedule",
						roundDur(gap)),
					Detail: "schedule: " + cj.Spec.Schedule,
					Since:  cj.Status.LastSuccessfulTime.Time,
				})
			}
		}
	}
	return out
}

func specReplicas(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}

// severityForShortfall escalates to Error when nothing is available at all,
// since partial capacity is degraded but total loss is an outage.
func severityForShortfall(have, want int32) health.Severity {
	if have == 0 && want > 0 {
		return health.SeverityError
	}
	return health.SeverityWarn
}
