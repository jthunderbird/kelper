package resources_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/resources"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRenderPodResourcesShowsLimits(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mypod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				},
			},
		},
	}
	var buf bytes.Buffer
	resources.RenderPod(&buf, pod)
	out := buf.String()
	if !strings.Contains(out, "500m") {
		t.Errorf("expected CPU limit '500m' in output, got: %s", out)
	}
}

func TestRenderPodResourcesShowsNoneWhenEmpty(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mypod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app"},
			},
		},
	}
	var buf bytes.Buffer
	resources.RenderPod(&buf, pod)
	out := buf.String()
	if !strings.Contains(out, "(none)") {
		t.Errorf("expected '(none)' for empty resources, got: %s", out)
	}
}
