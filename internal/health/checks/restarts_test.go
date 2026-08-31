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

func podWithRestart(name string, restarts int32, lastExit time.Time) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", Ready: true, RestartCount: restarts,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 1, Reason: "Error",
					FinishedAt: metav1.NewTime(lastExit),
				}},
			}},
		},
	}
}

func runRestarts(t *testing.T, pods ...corev1.Pod) []health.Finding {
	t.Helper()
	fs, err := checks.Restarts{}.Run(context.Background(), snapWithPods(pods...))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return fs
}

// The core case: the pod is Running and Ready right now, so a readiness check
// sees nothing, but it restarted inside the backoff window and will likely
// crash again.
func TestRunningPodInsideBackoffWindowIsReported(t *testing.T) {
	fs := runRestarts(t, podWithRestart("churn", 2, time.Now().Add(-2*time.Minute)))
	if len(fs) != 1 || fs[0].Reason != "RestartChurn" {
		t.Fatalf("expected RestartChurn, got %+v", fs)
	}
	if fs[0].Severity != health.SeverityWarn {
		t.Errorf("expected Warn for a low restart count, got %s", fs[0].Severity)
	}
}

// Restarts that happened long ago are history: the kubelet has reset its
// backoff and the container is stable.
func TestLongStablePodIsNotReported(t *testing.T) {
	fs := runRestarts(t, podWithRestart("stable", 7, time.Now().Add(-3*time.Hour)))
	if len(fs) != 0 {
		t.Fatalf("expected no findings for a long-stable pod, got %+v", fs)
	}
}

func TestSustainedChurnEscalatesToError(t *testing.T) {
	fs := runRestarts(t, podWithRestart("bad", 12, time.Now().Add(-30*time.Second)))
	if len(fs) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(fs))
	}
	if fs[0].Severity != health.SeverityError {
		t.Errorf("expected Error for sustained churn, got %s", fs[0].Severity)
	}
}

func TestNeverRestartedPodIsNotReported(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "fresh", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, RestartCount: 0}},
		},
	}
	if fs := runRestarts(t, pod); len(fs) != 0 {
		t.Fatalf("expected no findings, got %+v", fs)
	}
}
