package volumes_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/volumes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRenderPodVolumesShowsMounts(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mypod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					VolumeMounts: []corev1.VolumeMount{
						{Name: "data", MountPath: "/data"},
					},
				},
			},
			Volumes: []corev1.Volume{
				{Name: "data"},
			},
		},
	}
	var buf bytes.Buffer
	volumes.RenderPod(&buf, pod)
	out := buf.String()
	if !strings.Contains(out, "/data") {
		t.Errorf("expected mount path '/data' in output, got: %s", out)
	}
}

func TestRenderPodVolumesShowsNoneWhenEmpty(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mypod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app"},
			},
		},
	}
	var buf bytes.Buffer
	volumes.RenderPod(&buf, pod)
	out := buf.String()
	if !strings.Contains(out, "(none)") {
		t.Errorf("expected '(none)' for empty volumeMounts, got: %s", out)
	}
}
