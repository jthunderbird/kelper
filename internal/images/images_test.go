package images_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/images"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRenderPodImagesContainsImageRef(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mypod", Namespace: "default"},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{Name: "init", Image: "myrepo/init:v1"},
			},
			Containers: []corev1.Container{
				{Name: "app", Image: "myrepo/app:v2"},
			},
		},
	}
	var buf bytes.Buffer
	images.RenderPod(&buf, pod)
	out := buf.String()
	if !strings.Contains(out, "myrepo/init:v1") {
		t.Errorf("expected init container image in output, got: %s", out)
	}
	if !strings.Contains(out, "myrepo/app:v2") {
		t.Errorf("expected container image in output, got: %s", out)
	}
}

func TestRenderPodImagesShowsNoneForEmptyInitContainers(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mypod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "myrepo/app:v2"},
			},
		},
	}
	var buf bytes.Buffer
	images.RenderPod(&buf, pod)
	out := buf.String()
	if !strings.Contains(out, "(none)") {
		t.Errorf("expected '(none)' for empty initContainers, got: %s", out)
	}
}
