package sources

import (
	"context"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

// crdConcurrency bounds simultaneous custom-resource list calls.
const crdConcurrency = 8

// skipResources are resources that are either covered by a dedicated check or
// are high-cardinality noise that would dominate a scan.
var skipResources = map[string]bool{
	"events": true, "endpoints": true, "endpointslices": true, "leases": true,
	"secrets": true, "configmaps": true, "pods": true, "replicasets": true,
	"deployments": true, "statefulsets": true, "daemonsets": true, "jobs": true,
	"cronjobs": true, "nodes": true, "persistentvolumeclaims": true,
	"services": true, "certificatesigningrequests": true,
	"controllerrevisions": true, "podmetrics": true, "nodemetrics": true,
	"componentstatuses": true, "bindings": true, "tokenreviews": true,
	"localsubjectaccessreviews": true, "selfsubjectaccessreviews": true,
	"selfsubjectrulesreviews": true, "subjectaccessreviews": true,
	"selfsubjectreviews": true,
}

// CustomStatus is one custom-resource instance reduced to a health signal.
type CustomStatus struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
	// Ready is nil when the resource exposes no recognizable readiness signal.
	Ready   *bool
	Reason  string
	Message string
	Phase   string
}

// ScanCustomResources discovers every listable API resource — including custom
// resources and, importantly, cluster-scoped ones that a namespaced query never
// sees — and evaluates each instance's status conditions.
//
// There is no way to avoid one list call per resource type, so the work is
// bounded: a skip list, a worker pool, and a per-resource timeout. Resources the
// caller cannot list are silently skipped, since RBAC denial is expected.
func ScanCustomResources(ctx context.Context, disco discovery.DiscoveryInterface, dyn dynamic.Interface, namespace string, perResourceTimeout time.Duration) []CustomStatus {
	if disco == nil || dyn == nil {
		return nil
	}
	// Partial discovery is normal when an aggregated API is unhealthy; use
	// whatever groups did resolve.
	lists, err := disco.ServerPreferredResources()
	if err != nil && len(lists) == 0 {
		return nil
	}

	type target struct {
		gvr        schema.GroupVersionResource
		kind       string
		namespaced bool
	}
	var targets []target
	for _, list := range lists {
		if list == nil {
			continue
		}
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range list.APIResources {
			// Subresources are not independently listable.
			if strings.Contains(r.Name, "/") || skipResources[r.Name] {
				continue
			}
			if !supportsList(r.Verbs) {
				continue
			}
			targets = append(targets, target{
				gvr:        gv.WithResource(r.Name),
				kind:       r.Kind,
				namespaced: r.Namespaced,
			})
		}
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out []CustomStatus
		sem = make(chan struct{}, crdConcurrency)
	)
	for _, t := range targets {
		wg.Add(1)
		go func(t target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			rctx, cancel := context.WithTimeout(ctx, perResourceTimeout)
			defer cancel()

			var ri dynamic.ResourceInterface = dyn.Resource(t.gvr)
			// Cluster-scoped resources must not be namespace-qualified, which
			// is precisely why a namespaced-only scan misses them.
			if t.namespaced && namespace != "" {
				ri = dyn.Resource(t.gvr).Namespace(namespace)
			}

			list, err := ri.List(rctx, metav1.ListOptions{})
			if err != nil {
				return
			}
			var found []CustomStatus
			for i := range list.Items {
				if st, ok := evaluate(&list.Items[i], t.gvr.Group, t.kind); ok {
					found = append(found, st)
				}
			}
			if len(found) == 0 {
				return
			}
			mu.Lock()
			out = append(out, found...)
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return out
}

func supportsList(verbs metav1.Verbs) bool {
	for _, v := range verbs {
		if v == "list" {
			return true
		}
	}
	return false
}

// readyConditionTypes are the condition types that, when False, indicate an
// unhealthy resource. This convention is followed by cert-manager, Istio,
// ArgoCD, Flux and most other controllers, so one rule covers them all.
var readyConditionTypes = map[string]bool{
	"Ready": true, "Available": true, "Healthy": true, "Synced": true,
	"Established": true, "NamesAccepted": true,
}

// failedConditionTypes indicate trouble when True rather than False.
var failedConditionTypes = map[string]bool{
	"Degraded": true, "Failed": true, "Error": true, "ReconcileFailed": true,
}

// evaluate reduces an unstructured object to a health signal, returning false
// when it looks healthy or exposes no usable signal.
func evaluate(u *unstructured.Unstructured, group, kind string) (CustomStatus, bool) {
	st := CustomStatus{
		Group: group, Kind: kind,
		Namespace: u.GetNamespace(), Name: u.GetName(),
	}

	conds, found, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err == nil && found {
		for _, raw := range conds {
			cond, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			ctype, _ := cond["type"].(string)
			status, _ := cond["status"].(string)
			reason, _ := cond["reason"].(string)
			message, _ := cond["message"].(string)

			bad := (readyConditionTypes[ctype] && status == "False") ||
				(failedConditionTypes[ctype] && status == "True")
			if bad {
				no := false
				st.Ready, st.Reason, st.Message = &no, firstNonEmpty(reason, ctype), message
				return st, true
			}
		}
		// Conditions exist and none are bad.
		return st, false
	}

	// No conditions: fall back to a phase field, which some CRs use instead.
	phase, found, err := unstructured.NestedString(u.Object, "status", "phase")
	if err == nil && found && isUnhealthyPhase(phase) {
		no := false
		st.Ready, st.Phase = &no, phase
		return st, true
	}
	return st, false
}

func isUnhealthyPhase(phase string) bool {
	switch phase {
	case "Failed", "Error", "Unhealthy", "Degraded", "CrashLoopBackOff", "Lost":
		return true
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
