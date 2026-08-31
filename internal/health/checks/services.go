package checks

import (
	"context"
	"fmt"

	"github.com/jthunderbird/kelper/internal/health"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
)

// Services reports Services with no ready backing endpoints: the service
// resolves but every request to it fails.
type Services struct{}

// Name identifies the check.
func (Services) Name() string { return "services" }

// Description is one line of help text.
func (Services) Description() string {
	return "Services whose selector matches no ready pods"
}

// Requires reports no optional capabilities.
func (Services) Requires() []health.Capability { return nil }

// Run correlates each selector-backed Service with its EndpointSlices.
func (c Services) Run(ctx context.Context, s *health.Snapshot) ([]health.Finding, error) {
	// Count ready addresses per service from its EndpointSlices.
	ready := map[string]int{}
	for i := range s.EndpointSlices {
		es := &s.EndpointSlices[i]
		svc := es.Labels[discoveryv1.LabelServiceName]
		if svc == "" {
			continue
		}
		key := es.Namespace + "/" + svc
		if _, seen := ready[key]; !seen {
			ready[key] = 0
		}
		for _, ep := range es.Endpoints {
			// Conditions.Ready is a pointer; nil means ready by convention.
			if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
				ready[key] += len(ep.Addresses)
			}
		}
	}

	var out []health.Finding
	for i := range s.Services {
		svc := &s.Services[i]

		// ExternalName has no endpoints by design, and a selectorless service
		// is managed by hand, so absent endpoints are not a fault.
		if svc.Spec.Type == corev1.ServiceTypeExternalName || len(svc.Spec.Selector) == 0 {
			continue
		}
		// Headless services front stateful pods directly; an empty one during
		// scale-to-zero is normal.
		if svc.Spec.ClusterIP == corev1.ClusterIPNone {
			continue
		}

		if n, ok := ready[svc.Namespace+"/"+svc.Name]; ok && n > 0 {
			continue
		}

		out = append(out, health.Finding{
			Severity: health.SeverityError,
			Ref:      health.ResourceRef{Kind: "Service", Namespace: svc.Namespace, Name: svc.Name},
			Reason:   "NoReadyEndpoints",
			Message:  "service has no ready endpoints; traffic to it will fail",
			Detail:   fmt.Sprintf("selector: %v", svc.Spec.Selector),
			Remediation: "Check that pods matching the selector exist and are ready, " +
				"and that the selector labels are correct",
		})
	}
	return out, nil
}
