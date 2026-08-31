package health

import (
	"context"
	"fmt"
	"strings"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
)

// Capabilities records which optional data sources this cluster and credential
// can reach. Checks depending on a missing capability are skipped rather than
// failed, so kelper works against clusters without metrics-server or without
// node proxy permission.
type Capabilities struct {
	Metrics   bool
	NodeProxy bool
	Dynamic   bool
	// Degraded holds one human-readable line per unavailable source.
	Degraded []string
}

// Map renders capabilities in the form the snapshot stores.
func (c *Capabilities) Map() map[Capability]bool {
	return map[Capability]bool{
		CapMetrics:   c.Metrics,
		CapNodeProxy: c.NodeProxy,
		CapDynamic:   c.Dynamic,
	}
}

// DetectCapabilities probes the cluster for the optional sources. It never
// returns an error: an undetectable source is simply unavailable.
func DetectCapabilities(ctx context.Context, kube kubernetes.Interface, disco discovery.DiscoveryInterface) *Capabilities {
	c := &Capabilities{}

	if disco != nil {
		groups, err := disco.ServerGroups()
		switch {
		case err != nil:
			c.Degraded = append(c.Degraded, fmt.Sprintf("discovery unavailable (%s): custom resource checks skipped", short(err)))
		default:
			c.Dynamic = true
			for _, g := range groups.Groups {
				if g.Name == "metrics.k8s.io" {
					c.Metrics = true
					break
				}
			}
			if !c.Metrics {
				c.Degraded = append(c.Degraded, "metrics-server unavailable: CPU/memory limit checks skipped")
			}
		}
	} else {
		c.Degraded = append(c.Degraded, "discovery client not configured: custom resource checks skipped")
	}

	c.NodeProxy = canAccess(ctx, kube, "get", "", "nodes", "proxy")
	if !c.NodeProxy {
		c.Degraded = append(c.Degraded, "nodes/proxy denied: PVC usage and kubelet stats skipped")
	}

	return c
}

// canAccess runs a SelfSubjectAccessReview, treating any error as denial.
func canAccess(ctx context.Context, kube kubernetes.Interface, verb, group, resource, subresource string) bool {
	if kube == nil {
		return false
	}
	review := &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Verb:        verb,
				Group:       group,
				Resource:    resource,
				Subresource: subresource,
			},
		},
	}
	res, err := kube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false
	}
	return res.Status.Allowed
}

// short trims an error to its first line so degraded notes stay one line.
func short(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}
