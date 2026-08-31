package checks_test

import (
	"context"
	"testing"
	"time"

	"github.com/jthunderbird/kelper/internal/health"
	"github.com/jthunderbird/kelper/internal/health/checks"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func snapWithPods(pods ...corev1.Pod) *health.Snapshot {
	return health.NewSnapshotForTest(func(s *health.Snapshot) { s.Pods = pods })
}

func runPods(t *testing.T, pods ...corev1.Pod) []health.Finding {
	t.Helper()
	fs, err := checks.Pods{}.Run(context.Background(), snapWithPods(pods...))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return fs
}

func hasReason(fs []health.Finding, reason string) bool {
	for _, f := range fs {
		if f.Reason == reason {
			return true
		}
	}
	return false
}

// The previous implementation scanned only ContainerStatuses, so a pod that
// cannot be scheduled — and therefore has none — was reported as healthy.
func TestPendingUnschedulablePodIsReported(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "unschedulable", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  "Unschedulable",
				Message: "0/3 nodes are available: 3 Insufficient cpu.",
			}},
		},
	}
	fs := runPods(t, pod)
	if !hasReason(fs, "Unschedulable") {
		t.Fatalf("expected Unschedulable finding, got %+v", fs)
	}
}

func TestInitContainerImagePullIsReported(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-init", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name: "setup",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "ImagePullBackOff", Message: "manifest unknown",
				}},
			}},
		},
	}
	fs := runPods(t, pod)
	if !hasReason(fs, "ImagePullBackOff") {
		t.Fatalf("expected ImagePullBackOff on init container, got %+v", fs)
	}
}

// A completed init container is not-ready by design and must not be flagged.
func TestCompletedInitContainerIgnored(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name:  "setup",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
			}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true}},
		},
	}
	if fs := runPods(t, pod); len(fs) != 0 {
		t.Fatalf("expected no findings, got %+v", fs)
	}
}

// OOMKilled lives in LastTerminationState, so it is visible even while the
// container is running again.
func TestOOMKilledReportedWhileRunning(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "oom", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "app",
				Ready:        true,
				RestartCount: 3,
				State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "OOMKilled", ExitCode: 137,
					FinishedAt: metav1.NewTime(time.Now().Add(-time.Minute)),
				}},
			}},
		},
	}
	if !hasReason(runPods(t, pod), "OOMKilled") {
		t.Fatal("expected OOMKilled finding")
	}
}

func TestSucceededPodIsHealthy(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "done", Namespace: "default"},
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
	if fs := runPods(t, pod); len(fs) != 0 {
		t.Fatalf("expected no findings for Succeeded pod, got %+v", fs)
	}
}

func TestEvictedPodIsReported(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "eviction", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase:   corev1.PodFailed,
			Reason:  "Evicted",
			Message: "The node was low on resource: memory.",
		},
	}
	if !hasReason(runPods(t, pod), "Evicted") {
		t.Fatal("expected Evicted finding")
	}
}

func TestHealthyPodProducesNothing(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "good", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "a", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				{Name: "b", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	if fs := runPods(t, pod); len(fs) != 0 {
		t.Fatalf("expected no findings, got %+v", fs)
	}
}

func TestStuckTerminatingPodIsReported(t *testing.T) {
	del := metav1.NewTime(time.Now().Add(-30 * time.Minute))
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "stuck", Namespace: "default", DeletionTimestamp: &del,
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if !hasReason(runPods(t, pod), "StuckTerminating") {
		t.Fatal("expected StuckTerminating finding")
	}
}
