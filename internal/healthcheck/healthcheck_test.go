package healthcheck_test

import (
	"testing"

	"github.com/jthunderbird/kelper/internal/healthcheck"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsUnhealthyPodDetectsCrashLoop(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-pod", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: false, State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				}},
			},
		},
	}
	if !healthcheck.IsUnhealthyPod(pod) {
		t.Error("expected CrashLoopBackOff pod to be unhealthy")
	}
}

func TestIsUnhealthyPodIgnoresCompleted(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "done-pod", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodSucceeded,
		},
	}
	if healthcheck.IsUnhealthyPod(pod) {
		t.Error("expected Succeeded pod to be considered healthy")
	}
}

func TestIsUnhealthyPodAllContainersReady(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "good-pod", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true},
				{Ready: true},
			},
		},
	}
	if healthcheck.IsUnhealthyPod(pod) {
		t.Error("expected all-ready pod to be healthy")
	}
}
