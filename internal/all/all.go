// Package all implements `kelper all`: list every resource type the cluster
// serves, custom resources included, which `kubectl get all` does not.
//
// Selection, ordering and rendering are independent of the cluster; a Source
// supplies discovery and listings so they can be tested with a fake.
package all

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// concurrency bounds simultaneous list calls.
const concurrency = 8

// Resource is one listable API resource type.
type Resource struct {
	GVR        schema.GroupVersionResource
	Kind       string
	ShortNames []string
	Namespaced bool
}

// FullName is the resource name qualified by its group, as kubectl
// api-resources prints it: "pods", "deployments.apps".
func (r Resource) FullName() string {
	if r.GVR.Group == "" {
		return r.GVR.Resource
	}
	return r.GVR.Resource + "." + r.GVR.Group
}

// KindName is how `kubectl -o name` prefixes an object: "pod", "deployment.apps".
func (r Resource) KindName() string {
	k := strings.ToLower(r.Kind)
	if r.GVR.Group == "" {
		return k
	}
	return k + "." + r.GVR.Group
}

// Source supplies discovery and listings.
type Source interface {
	// Resources returns every listable type. A partial result alongside an
	// error is fine: aggregated APIs are often unhealthy.
	Resources(ctx context.Context) ([]Resource, error)
	// Table lists a type in kubectl's table form. namespace is "" for every
	// namespace (and for cluster-scoped types).
	Table(ctx context.Context, r Resource, namespace, selector string) (*Table, error)
	// Objects lists a type's objects in full.
	Objects(ctx context.Context, r Resource, namespace, selector string) ([]unstructured.Unstructured, error)
}

// Table is the part of a server-side table kelper renders.
type Table struct {
	Columns []Column
	Rows    []Row
}

// Column is a table column. Priority 0 columns show by default; the rest only
// with -o wide.
type Column struct {
	Name     string
	Priority int32
}

// Row is one object's cells plus its identity.
type Row struct {
	Cells     []string
	Namespace string
	Name      string
}

// Options configure a run.
type Options struct {
	// Namespace is "" for every namespace, which also brings in cluster-scoped
	// types.
	Namespace    string
	Selector     string
	Include      []string
	Exclude      []string
	IncludeNoisy bool
	// Output is "", "wide", "name", "yaml" or "json".
	Output string
}

// noisy types are left out unless asked for: they dwarf everything else.
var noisy = map[string]bool{"events": true, "leases": true}

// Select applies the request to the discovered types and returns them in
// display order: core group first, then groups alphabetically.
func Select(all []Resource, o Options) []Resource {
	include, exclude := normalize(o.Include), normalize(o.Exclude)
	coreEvents := false
	for _, r := range all {
		if r.GVR.Resource == "events" && r.GVR.Group == "" {
			coreEvents = true
		}
	}
	var out []Resource
	for _, r := range all {
		if o.Namespace != "" && !r.Namespaced {
			continue
		}
		// Core and events.k8s.io serve the same events; keep the core one.
		if coreEvents && r.GVR.Resource == "events" && r.GVR.Group == "events.k8s.io" {
			continue
		}
		explicit := len(include) > 0 && matches(r, include)
		if len(include) > 0 && !explicit {
			continue
		}
		if matches(r, exclude) {
			continue
		}
		if !explicit && !o.IncludeNoisy && noisy[r.GVR.Resource] {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.GVR.Group == "") != (b.GVR.Group == "") {
			return a.GVR.Group == ""
		}
		if a.GVR.Group != b.GVR.Group {
			return a.GVR.Group < b.GVR.Group
		}
		return a.GVR.Resource < b.GVR.Resource
	})
	return out
}

func normalize(in []string) []string {
	var out []string
	for _, s := range in {
		for _, p := range strings.Split(s, ",") {
			if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// matches reports whether any name refers to r: plural, plural.group, kind or
// a short name.
func matches(r Resource, names []string) bool {
	for _, n := range names {
		switch n {
		case r.GVR.Resource, strings.ToLower(r.FullName()), strings.ToLower(r.Kind):
			return true
		}
		for _, s := range r.ShortNames {
			if n == strings.ToLower(s) {
				return true
			}
		}
	}
	return false
}

// result is one type's listing, or why it failed.
type result struct {
	res   Resource
	table *Table
	objs  []unstructured.Unstructured
	err   error
}

// Fetch lists the selected types with bounded concurrency, keeping their order.
func Fetch(ctx context.Context, src Source, types []Resource, o Options) []result {
	out := make([]result, len(types))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, r := range types {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, r Resource) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i].res = r
			switch o.Output {
			case "yaml", "json":
				out[i].objs, out[i].err = src.Objects(ctx, r, scope(r, o), o.Selector)
			default:
				out[i].table, out[i].err = src.Table(ctx, r, scope(r, o), o.Selector)
			}
		}(i, r)
	}
	wg.Wait()
	return out
}

func scope(r Resource, o Options) string {
	if !r.Namespaced {
		return ""
	}
	return o.Namespace
}

// Skipped describes the types that could not be listed, for the final line.
func Skipped(results []result) []string {
	var out []string
	for _, r := range results {
		if r.err != nil {
			out = append(out, fmt.Sprintf("%s (%s)", r.res.FullName(), short(r.err)))
		}
	}
	return out
}

func short(err error) string {
	msg := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 80 {
		msg = msg[:77] + "..."
	}
	return msg
}
