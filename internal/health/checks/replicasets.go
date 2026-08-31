package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jthunderbird/kelper/internal/health"
	appsv1 "k8s.io/api/apps/v1"
)

// revisionAnnotation is set by the deployment controller on both the Deployment
// and each of its ReplicaSets, and is how a ReplicaSet is matched to the
// revision it belongs to.
const revisionAnnotation = "deployment.kubernetes.io/revision"

// ReplicaSets reports ReplicaSets that still hold pods but are no longer the
// current revision of their Deployment, and ReplicaSets whose owner is gone.
// A scaled-to-zero old ReplicaSet is normal rollout history and is ignored.
type ReplicaSets struct{}

// Name identifies the check.
func (ReplicaSets) Name() string { return "replicasets" }

// Description is one line of help text.
func (ReplicaSets) Description() string {
	return "Stale ReplicaSets still running pods, and ReplicaSets orphaned by a deleted Deployment"
}

// Requires reports no optional capabilities.
func (ReplicaSets) Requires() []health.Capability { return nil }

// Run correlates each ReplicaSet with its owning Deployment.
func (c ReplicaSets) Run(ctx context.Context, s *health.Snapshot) ([]health.Finding, error) {
	deploys := map[string]*appsv1.Deployment{}
	for i := range s.Deployments {
		d := &s.Deployments[i]
		deploys[d.Namespace+"/"+d.Name] = d
	}

	var out []health.Finding
	for i := range s.ReplicaSets {
		rs := &s.ReplicaSets[i]

		// Only ReplicaSets actually running pods matter; retained history at
		// zero replicas is expected and must not be reported.
		replicas := rs.Status.Replicas
		if replicas == 0 {
			continue
		}

		ref := health.ResourceRef{Group: "apps", Kind: "ReplicaSet", Namespace: rs.Namespace, Name: rs.Name}

		owner := ownerDeployment(rs)
		if owner == "" {
			// No controller owner at all: either hand-created or the owner
			// reference was stripped.
			out = append(out, health.Finding{
				Severity:    health.SeverityWarn,
				Ref:         ref,
				Reason:      "OrphanedReplicaSet",
				Message:     fmt.Sprintf("%d pod(s) running with no owning Deployment", replicas),
				Remediation: "Delete it if it is leftover, or adopt it under a Deployment",
			})
			continue
		}

		d, ok := deploys[rs.Namespace+"/"+owner]
		if !ok {
			// The owner is named but absent. Within a namespaced snapshot this
			// is conclusive; garbage collection should have removed it.
			out = append(out, health.Finding{
				Severity:    health.SeverityError,
				Ref:         ref,
				Reason:      "OwnerMissing",
				Message:     fmt.Sprintf("%d pod(s) running but Deployment %q no longer exists", replicas, owner),
				Remediation: "Garbage collection may be stalled; check kube-controller-manager",
			})
			continue
		}

		cur, rsRev := d.Annotations[revisionAnnotation], rs.Annotations[revisionAnnotation]
		if cur == "" || rsRev == "" || cur == rsRev {
			continue
		}

		// A non-current ReplicaSet still holding pods means the previous
		// revision never drained: a rollout that stalled part-way.
		sev := health.SeverityWarn
		if d.Status.UpdatedReplicas == 0 {
			sev = health.SeverityError
		}
		out = append(out, health.Finding{
			Severity: sev,
			Ref:      ref,
			Reason:   "StaleReplicaSet",
			Message: fmt.Sprintf("revision %s still running %d pod(s); Deployment %q is at revision %s",
				rsRev, replicas, owner, cur),
			Detail:      "An old ReplicaSet that never scaled down usually means the new revision could not become ready.",
			Remediation: "Check the current ReplicaSet's pods, then: kubectl rollout status deployment/" + owner,
			Evidence: []health.Evidence{
				{Label: "replicaset revision", Value: rsRev},
				{Label: "deployment revision", Value: cur},
				{Label: "pods still running", Value: fmt.Sprint(replicas)},
				{Label: "age", Value: roundDur(time.Since(rs.CreationTimestamp.Time)).String()},
			},
			Since: rs.CreationTimestamp.Time,
		})
	}
	return out, nil
}

// ownerDeployment returns the name of the controlling Deployment, or "".
func ownerDeployment(rs *appsv1.ReplicaSet) string {
	for _, o := range rs.OwnerReferences {
		if o.Kind == "Deployment" && o.Controller != nil && *o.Controller {
			return o.Name
		}
	}
	return ""
}
