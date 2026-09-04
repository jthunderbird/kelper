package health_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/health"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes/fake"
	openapi "k8s.io/client-go/openapi"
	restclient "k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// stubDiscovery reports a fixed set of API groups.
type stubDiscovery struct {
	discovery.DiscoveryInterface
	groups []string
}

func (s *stubDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	list := &metav1.APIGroupList{}
	for _, g := range s.groups {
		list.Groups = append(list.Groups, metav1.APIGroup{Name: g})
	}
	return list, nil
}

func (s *stubDiscovery) ServerVersion() (*version.Info, error) { return &version.Info{}, nil }
func (s *stubDiscovery) RESTClient() restclient.Interface      { return nil }
func (s *stubDiscovery) OpenAPIV3() openapi.Client             { return nil }

// allowFunc decides SelfSubjectAccessReview outcomes for a fake clientset.
func clientAllowing(allow func(*authv1.ResourceAttributes) bool) *fake.Clientset {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "selfsubjectaccessreviews",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			obj := action.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
			obj.Status.Allowed = allow(obj.Spec.ResourceAttributes)
			return true, obj, nil
		})
	return cs
}

func degradedContains(caps *health.Capabilities, substr string) bool {
	for _, d := range caps.Degraded {
		if strings.Contains(d, substr) {
			return true
		}
	}
	return false
}

func TestCapabilitiesAllAvailable(t *testing.T) {
	cs := clientAllowing(func(*authv1.ResourceAttributes) bool { return true })
	disco := &stubDiscovery{groups: []string{"apps", "metrics.k8s.io"}}

	caps := health.DetectCapabilities(context.Background(), cs, disco)
	if !caps.Metrics || !caps.NodeProxy || !caps.Dynamic {
		t.Fatalf("expected all capabilities, got %+v", caps)
	}
	if len(caps.Degraded) != 0 {
		t.Errorf("expected no degraded notes, got %v", caps.Degraded)
	}
}

func TestMissingMetricsServerDegrades(t *testing.T) {
	cs := clientAllowing(func(*authv1.ResourceAttributes) bool { return true })
	disco := &stubDiscovery{groups: []string{"apps"}}

	caps := health.DetectCapabilities(context.Background(), cs, disco)
	if caps.Metrics {
		t.Error("expected metrics to be unavailable")
	}
	if !degradedContains(caps, "metrics-server unavailable") {
		t.Errorf("expected a metrics-server note, got %v", caps.Degraded)
	}
}

// The metrics API is served but this credential may not read it. Discovery is
// readable by any authenticated user, so presence of the API group alone would
// wrongly enable the limit checks, which would then silently find nothing.
func TestMetricsServedButForbiddenDegrades(t *testing.T) {
	cs := clientAllowing(func(ra *authv1.ResourceAttributes) bool {
		return ra.Group != "metrics.k8s.io"
	})
	disco := &stubDiscovery{groups: []string{"apps", "metrics.k8s.io"}}

	caps := health.DetectCapabilities(context.Background(), cs, disco)
	if caps.Metrics {
		t.Error("expected metrics to be treated as unavailable when forbidden")
	}
	if !degradedContains(caps, "metrics.k8s.io denied") {
		t.Errorf("expected a denial note, got %v", caps.Degraded)
	}
}

func TestNodeProxyDeniedDegrades(t *testing.T) {
	cs := clientAllowing(func(ra *authv1.ResourceAttributes) bool {
		return ra.Subresource != "proxy"
	})
	disco := &stubDiscovery{groups: []string{"apps", "metrics.k8s.io"}}

	caps := health.DetectCapabilities(context.Background(), cs, disco)
	if caps.NodeProxy {
		t.Error("expected node proxy to be unavailable")
	}
	if !degradedContains(caps, "nodes/proxy denied") {
		t.Errorf("expected a nodes/proxy note, got %v", caps.Degraded)
	}
}

// A check requiring a missing capability must be skipped with a reason rather
// than run against absent data.
func TestChecksRequiringMissingCapabilityAreSkipped(t *testing.T) {
	s := health.NewSnapshotForTest()
	if _, ok := s.Missing([]health.Capability{health.CapMetrics}); !ok {
		t.Fatal("test snapshot should have all capabilities")
	}

	res := health.Run(context.Background(), []health.Check{needsMetrics{}}, snapshotWithout(health.CapMetrics))
	if len(res.Findings) != 0 {
		t.Errorf("expected no findings from a skipped check, got %+v", res.Findings)
	}
	if reason, ok := res.Skipped["needs-metrics"]; !ok {
		t.Error("expected the check to be recorded as skipped")
	} else if !strings.Contains(reason, "metrics") {
		t.Errorf("expected the reason to name metrics, got %q", reason)
	}
}

// snapshotWithout builds a snapshot lacking one capability.
func snapshotWithout(missing health.Capability) *health.Snapshot {
	return health.NewSnapshotForTestWithCaps(map[health.Capability]bool{
		health.CapMetrics:   missing != health.CapMetrics,
		health.CapNodeProxy: missing != health.CapNodeProxy,
		health.CapDynamic:   missing != health.CapDynamic,
	})
}

type needsMetrics struct{}

func (needsMetrics) Name() string        { return "needs-metrics" }
func (needsMetrics) Description() string { return "test check" }
func (needsMetrics) Requires() []health.Capability {
	return []health.Capability{health.CapMetrics}
}
func (needsMetrics) Run(context.Context, *health.Snapshot) ([]health.Finding, error) {
	return []health.Finding{{Reason: "ShouldNotRun"}}, nil
}
