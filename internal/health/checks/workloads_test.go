package checks_test

import (
	"context"
	"testing"
	"time"

	"github.com/jthunderbird/kelper/internal/health"
	"github.com/jthunderbird/kelper/internal/health/checks"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func runWorkloads(t *testing.T, mutate func(*health.Snapshot)) []health.Finding {
	t.Helper()
	fs, err := checks.Workloads{}.Run(context.Background(), health.NewSnapshotForTest(mutate))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return fs
}

func i32(v int32) *int32 { return &v }

// A deployment at full replica count whose rollout has stalled was previously
// invisible: the old check only compared available against desired.
func TestStalledRolloutAtFullReplicasIsReported(t *testing.T) {
	fs := runWorkloads(t, func(s *health.Snapshot) {
		s.Deployments = []appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
			Spec:       appsv1.DeploymentSpec{Replicas: i32(3)},
			Status: appsv1.DeploymentStatus{
				AvailableReplicas: 3,
				Conditions: []appsv1.DeploymentCondition{{
					Type:    appsv1.DeploymentProgressing,
					Status:  corev1.ConditionFalse,
					Reason:  "ProgressDeadlineExceeded",
					Message: "ReplicaSet \"web-abc\" has timed out progressing.",
				}},
			},
		}}
	})
	if !hasReason(fs, "ProgressDeadlineExceeded") {
		t.Fatalf("expected a stalled-rollout finding, got %+v", fs)
	}
}

func TestDeploymentReplicaShortfallIsReported(t *testing.T) {
	fs := runWorkloads(t, func(s *health.Snapshot) {
		s.Deployments = []appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
			Spec:       appsv1.DeploymentSpec{Replicas: i32(3)},
			Status:     appsv1.DeploymentStatus{AvailableReplicas: 1},
		}}
	})
	if !hasReason(fs, "ReplicasUnavailable") {
		t.Fatalf("expected ReplicasUnavailable, got %+v", fs)
	}
}

// Regression: a failed Job used to be reported forever. Only recent failures
// are actionable.
func TestOldFailedJobIsNotReported(t *testing.T) {
	fs := runWorkloads(t, func(s *health.Snapshot) {
		s.Jobs = []batchv1.Job{{
			ObjectMeta: metav1.ObjectMeta{Name: "ancient", Namespace: "default"},
			Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
				Type: batchv1.JobFailed, Status: corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-30 * 24 * time.Hour)),
			}}},
		}}
	})
	if len(fs) != 0 {
		t.Fatalf("expected no findings for a month-old failed job, got %+v", fs)
	}
}

func TestRecentFailedJobIsReported(t *testing.T) {
	fs := runWorkloads(t, func(s *health.Snapshot) {
		s.Jobs = []batchv1.Job{{
			ObjectMeta: metav1.ObjectMeta{Name: "recent", Namespace: "default"},
			Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
				Type: batchv1.JobFailed, Status: corev1.ConditionTrue,
				Reason:             "BackoffLimitExceeded",
				LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour)),
			}}},
		}}
	})
	if !hasReason(fs, "BackoffLimitExceeded") {
		t.Fatalf("expected a recent failed-job finding, got %+v", fs)
	}
}

func TestCompletedJobIsHealthy(t *testing.T) {
	fs := runWorkloads(t, func(s *health.Snapshot) {
		s.Jobs = []batchv1.Job{{
			ObjectMeta: metav1.ObjectMeta{Name: "done", Namespace: "default"},
			Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
				Type: batchv1.JobComplete, Status: corev1.ConditionTrue,
			}}},
		}}
	})
	if len(fs) != 0 {
		t.Fatalf("expected no findings for a completed job, got %+v", fs)
	}
}

// Regression: CronJobs were listed and the result thrown away.
func TestSuspendedCronJobIsReported(t *testing.T) {
	susp := true
	fs := runWorkloads(t, func(s *health.Snapshot) {
		s.CronJobs = []batchv1.CronJob{{
			ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "default"},
			Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: &susp},
		}}
	})
	if !hasReason(fs, "Suspended") {
		t.Fatalf("expected a Suspended cronjob finding, got %+v", fs)
	}
}

func TestNeverScheduledCronJobIsReported(t *testing.T) {
	fs := runWorkloads(t, func(s *health.Snapshot) {
		s.CronJobs = []batchv1.CronJob{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "broken", Namespace: "default",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-72 * time.Hour)),
			},
			Spec: batchv1.CronJobSpec{Schedule: "bogus"},
		}}
	})
	if !hasReason(fs, "NeverScheduled") {
		t.Fatalf("expected NeverScheduled, got %+v", fs)
	}
}

// A StatefulSet mid-rollout keeps two revisions live; ordered rollouts stall
// silently, which replica counts alone do not reveal.
func TestStatefulSetStuckRolloutIsReported(t *testing.T) {
	fs := runWorkloads(t, func(s *health.Snapshot) {
		s.StatefulSets = []appsv1.StatefulSet{{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"},
			Spec:       appsv1.StatefulSetSpec{Replicas: i32(3)},
			Status: appsv1.StatefulSetStatus{
				ReadyReplicas: 3, UpdatedReplicas: 1,
				CurrentRevision: "db-1", UpdateRevision: "db-2",
			},
		}}
	})
	if !hasReason(fs, "RolloutIncomplete") {
		t.Fatalf("expected RolloutIncomplete, got %+v", fs)
	}
}

func TestDaemonSetMisscheduledIsReported(t *testing.T) {
	fs := runWorkloads(t, func(s *health.Snapshot) {
		s.DaemonSets = []appsv1.DaemonSet{{
			ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "kube-system"},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 3, NumberReady: 3,
				UpdatedNumberScheduled: 3, NumberMisscheduled: 1,
			},
		}}
	})
	if !hasReason(fs, "Misscheduled") {
		t.Fatalf("expected Misscheduled, got %+v", fs)
	}
}

func TestHealthyWorkloadsProduceNothing(t *testing.T) {
	fs := runWorkloads(t, func(s *health.Snapshot) {
		s.Deployments = []appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
			Spec:       appsv1.DeploymentSpec{Replicas: i32(2)},
			Status:     appsv1.DeploymentStatus{AvailableReplicas: 2, UpdatedReplicas: 2},
		}}
		s.DaemonSets = []appsv1.DaemonSet{{
			ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "kube-system"},
			Status: appsv1.DaemonSetStatus{
				DesiredNumberScheduled: 3, NumberReady: 3, UpdatedNumberScheduled: 3,
			},
		}}
	})
	if len(fs) != 0 {
		t.Fatalf("expected no findings for healthy workloads, got %+v", fs)
	}
}
