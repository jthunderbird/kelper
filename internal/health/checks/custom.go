package checks

import (
	"context"
	"fmt"

	"github.com/jthunderbird/kelper/internal/health"
)

// CustomResources reports custom resources whose status conditions say they are
// unhealthy. A single generic rule covers every controller that follows the
// standard condition convention, and the scan includes cluster-scoped resources
// that namespaced queries never see.
type CustomResources struct{}

// Name identifies the check.
func (CustomResources) Name() string { return "custom" }

// Description is one line of help text.
func (CustomResources) Description() string {
	return "Custom resources reporting a failing Ready/Available/Healthy condition"
}

// Requires discovery and dynamic listing.
func (CustomResources) Requires() []health.Capability {
	return []health.Capability{health.CapDynamic}
}

// Run turns collected custom-resource statuses into findings.
func (c CustomResources) Run(ctx context.Context, s *health.Snapshot) ([]health.Finding, error) {
	var out []health.Finding
	for _, cr := range s.Custom {
		if cr.Ready != nil && *cr.Ready {
			continue
		}
		msg := cr.Message
		if msg == "" {
			if cr.Phase != "" {
				msg = "phase: " + cr.Phase
			} else {
				msg = "reported not ready"
			}
		}
		out = append(out, health.Finding{
			Severity: health.SeverityWarn,
			Ref: health.ResourceRef{
				Group: cr.Group, Kind: cr.Kind,
				Namespace: cr.Namespace, Name: cr.Name,
			},
			Reason:      firstNonEmpty(cr.Reason, "NotReady"),
			Message:     fmt.Sprintf("%s is not ready: %s", cr.Kind, msg),
			Detail:      cr.Message,
			Remediation: "Check the controller managing this resource",
		})
	}
	return out, nil
}
