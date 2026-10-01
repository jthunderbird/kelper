package health_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jthunderbird/kelper/internal/health"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func testPod() *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"}}
}

func snapshotDegradedContains(s *health.Snapshot, substr string) bool {
	for _, d := range s.Degraded() {
		if strings.Contains(d, substr) {
			return true
		}
	}
	return false
}

// An unreachable cluster fails every list. That must surface as an error, not
// an empty snapshot that renders as a healthy cluster.
func TestListProviderErrorsWhenEveryListFails(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("dial tcp 10.0.0.1:6443: connect: connection refused")
	})

	p := &health.ListProvider{Kube: cs}
	if _, err := p.Snapshot(context.Background()); err == nil {
		t.Fatal("expected an error when no resource could be listed")
	}
}

// RBAC denying a subset of resources is degradation, not failure.
func TestListProviderDegradesOnPartialFailure(t *testing.T) {
	cs := fake.NewSimpleClientset(testPod())
	cs.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", errors.New("denied"))
	})

	p := &health.ListProvider{Kube: cs}
	s, err := p.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.Pods) != 1 {
		t.Errorf("expected 1 pod, got %d", len(s.Pods))
	}
	if !snapshotDegradedContains(s, "nodes: list denied") {
		t.Errorf("expected a degraded note for nodes, got %v", s.Degraded())
	}
}

// A resource the credential may not watch must be skipped rather than started:
// its informer would never sync and the dashboard would hang.
func TestInformerProviderSkipsDeniedResources(t *testing.T) {
	cs := fake.NewSimpleClientset(testPod())
	allowNotNodes := func(a *authv1.ResourceAttributes) bool { return a.Resource != "nodes" }
	cs.PrependReactor("create", "selfsubjectaccessreviews",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			obj := action.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
			obj.Status.Allowed = allowNotNodes(obj.Spec.ResourceAttributes)
			return true, obj, nil
		})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := health.NewInformerProvider(ctx, cs, "", &health.Capabilities{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s, err := p.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(s.Pods) != 1 {
		t.Errorf("expected 1 pod, got %d", len(s.Pods))
	}
	if !snapshotDegradedContains(s, "nodes: list/watch denied") {
		t.Errorf("expected a degraded note for nodes, got %v", s.Degraded())
	}
}

func TestInformerProviderErrorsWhenNothingIsReadable(t *testing.T) {
	cs := clientAllowing(func(*authv1.ResourceAttributes) bool { return false })
	if _, err := health.NewInformerProvider(context.Background(), cs, "", nil); err == nil {
		t.Fatal("expected an error when every resource is denied")
	}
}

func TestInformerProviderErrorsWhenClusterUnreachable(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	_, err := health.NewInformerProvider(context.Background(), cs, "", nil)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected the review error to surface, got %v", err)
	}
}
