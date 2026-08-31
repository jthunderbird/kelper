package checks_test

import (
	"context"
	"testing"

	"github.com/jthunderbird/kelper/internal/health"
	"github.com/jthunderbird/kelper/internal/health/checks"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func deployment(name, revision string, updated int32) appsv1.Deployment {
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default",
			Annotations: map[string]string{"deployment.kubernetes.io/revision": revision},
		},
		Status: appsv1.DeploymentStatus{UpdatedReplicas: updated},
	}
}

func replicaSet(name, revision, owner string, replicas int32) appsv1.ReplicaSet {
	rs := appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default",
			Annotations: map[string]string{"deployment.kubernetes.io/revision": revision},
		},
		Status: appsv1.ReplicaSetStatus{Replicas: replicas},
	}
	if owner != "" {
		ctrl := true
		rs.OwnerReferences = []metav1.OwnerReference{
			{Kind: "Deployment", Name: owner, Controller: &ctrl},
		}
	}
	return rs
}

func runRS(t *testing.T, ds []appsv1.Deployment, rss []appsv1.ReplicaSet) []health.Finding {
	t.Helper()
	s := health.NewSnapshotForTest(func(s *health.Snapshot) {
		s.Deployments, s.ReplicaSets = ds, rss
	})
	fs, err := checks.ReplicaSets{}.Run(context.Background(), s)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return fs
}

func TestStaleReplicaSetWithPodsIsReported(t *testing.T) {
	fs := runRS(t,
		[]appsv1.Deployment{deployment("web", "3", 1)},
		[]appsv1.ReplicaSet{replicaSet("web-old", "2", "web", 2)},
	)
	if len(fs) != 1 || fs[0].Reason != "StaleReplicaSet" {
		t.Fatalf("expected StaleReplicaSet, got %+v", fs)
	}
}

// Retained rollout history at zero replicas is normal and must stay quiet,
// otherwise every cluster reports dozens of false positives.
func TestScaledToZeroHistoryIsIgnored(t *testing.T) {
	fs := runRS(t,
		[]appsv1.Deployment{deployment("web", "3", 3)},
		[]appsv1.ReplicaSet{replicaSet("web-old", "2", "web", 0)},
	)
	if len(fs) != 0 {
		t.Fatalf("expected no findings for zero-replica history, got %+v", fs)
	}
}

func TestCurrentReplicaSetIsIgnored(t *testing.T) {
	fs := runRS(t,
		[]appsv1.Deployment{deployment("web", "3", 3)},
		[]appsv1.ReplicaSet{replicaSet("web-cur", "3", "web", 3)},
	)
	if len(fs) != 0 {
		t.Fatalf("expected no findings for the current ReplicaSet, got %+v", fs)
	}
}

func TestOrphanedReplicaSetIsReported(t *testing.T) {
	fs := runRS(t, nil, []appsv1.ReplicaSet{replicaSet("loose", "1", "", 2)})
	if len(fs) != 1 || fs[0].Reason != "OrphanedReplicaSet" {
		t.Fatalf("expected OrphanedReplicaSet, got %+v", fs)
	}
}

func TestMissingOwnerIsReported(t *testing.T) {
	fs := runRS(t, nil, []appsv1.ReplicaSet{replicaSet("ghost", "1", "deleted-deploy", 1)})
	if len(fs) != 1 || fs[0].Reason != "OwnerMissing" {
		t.Fatalf("expected OwnerMissing, got %+v", fs)
	}
}

// A rollout where nothing new became ready is worse than a partial one.
func TestStaleWithNoUpdatedReplicasIsError(t *testing.T) {
	fs := runRS(t,
		[]appsv1.Deployment{deployment("web", "3", 0)},
		[]appsv1.ReplicaSet{replicaSet("web-old", "2", "web", 2)},
	)
	if len(fs) != 1 || fs[0].Severity != health.SeverityError {
		t.Fatalf("expected an Error-severity finding, got %+v", fs)
	}
}
