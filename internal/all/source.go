package all

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// tableAccept asks the API server for kubectl's table form, so each type shows
// its own printer columns, custom resources included.
const tableAccept = "application/json;as=Table;v=v1;g=meta.k8s.io,application/json"

// listTimeout bounds one list call.
const listTimeout = 30 * time.Second

// APISource is the Source backed by a live cluster.
type APISource struct {
	Discovery discovery.DiscoveryInterface
	REST      rest.Interface
	Dynamic   dynamic.Interface
}

// NewAPISource builds the source from a rest config.
func NewAPISource(cfg *rest.Config) (*APISource, error) {
	d, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &APISource{Discovery: d, REST: d.RESTClient(), Dynamic: dyn}, nil
}

// Resources implements Source.
func (s *APISource) Resources(ctx context.Context) ([]Resource, error) {
	lists, err := s.Discovery.ServerPreferredResources()
	var out []Resource
	for _, list := range lists {
		if list == nil {
			continue
		}
		gv, perr := schema.ParseGroupVersion(list.GroupVersion)
		if perr != nil {
			continue
		}
		for _, r := range list.APIResources {
			if strings.Contains(r.Name, "/") || !hasVerb(r.Verbs, "list") {
				continue
			}
			out = append(out, Resource{
				GVR:        gv.WithResource(r.Name),
				Kind:       r.Kind,
				ShortNames: r.ShortNames,
				Namespaced: r.Namespaced,
			})
		}
	}
	// Partial discovery is normal when an aggregated API is down; the caller
	// reports err and carries on with what resolved.
	return out, err
}

func hasVerb(verbs metav1.Verbs, want string) bool {
	for _, v := range verbs {
		if v == want {
			return true
		}
	}
	return false
}

func apiPath(r Resource, namespace string) string {
	base := "/apis/" + r.GVR.Group + "/" + r.GVR.Version
	if r.GVR.Group == "" {
		base = "/api/" + r.GVR.Version
	}
	if namespace != "" {
		base += "/namespaces/" + namespace
	}
	return base + "/" + r.GVR.Resource
}

// Table implements Source.
func (s *APISource) Table(ctx context.Context, r Resource, namespace, selector string) (*Table, error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	req := s.REST.Get().AbsPath(apiPath(r, namespace)).SetHeader("Accept", tableAccept)
	if selector != "" {
		req = req.Param("labelSelector", selector)
	}
	raw, err := req.DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	return ParseTable(raw)
}

// ParseTable reads a server table. APIs that ignore the table request answer
// with a plain list; those get a single NAME column.
func ParseTable(raw []byte) (*Table, error) {
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if probe.Kind != "Table" {
		var list struct {
			Items []struct {
				Metadata struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"metadata"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
		t := &Table{Columns: []Column{{Name: "Name"}}}
		for _, it := range list.Items {
			t.Rows = append(t.Rows, Row{Cells: []string{it.Metadata.Name}, Name: it.Metadata.Name, Namespace: it.Metadata.Namespace})
		}
		return t, nil
	}

	var tbl struct {
		Columns []struct {
			Name     string `json:"name"`
			Priority int32  `json:"priority"`
		} `json:"columnDefinitions"`
		Rows []struct {
			Cells  []any `json:"cells"`
			Object struct {
				Metadata struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"metadata"`
			} `json:"object"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(raw, &tbl); err != nil {
		return nil, fmt.Errorf("decode table: %w", err)
	}
	t := &Table{}
	for _, c := range tbl.Columns {
		t.Columns = append(t.Columns, Column{Name: c.Name, Priority: c.Priority})
	}
	for _, row := range tbl.Rows {
		cells := make([]string, len(row.Cells))
		for i, c := range row.Cells {
			cells[i] = cell(c)
		}
		t.Rows = append(t.Rows, Row{Cells: cells, Name: row.Object.Metadata.Name, Namespace: row.Object.Metadata.Namespace})
	}
	return t, nil
}

func cell(v any) string {
	switch x := v.(type) {
	case nil:
		// kubectl prints a missing value as an empty cell.
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// Objects implements Source.
func (s *APISource) Objects(ctx context.Context, r Resource, namespace, selector string) ([]unstructured.Unstructured, error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	var ri dynamic.ResourceInterface = s.Dynamic.Resource(r.GVR)
	if namespace != "" {
		ri = s.Dynamic.Resource(r.GVR).Namespace(namespace)
	}
	list, err := ri.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		// A list response leaves kind and apiVersion off its items.
		if list.Items[i].GetKind() == "" {
			list.Items[i].SetKind(r.Kind)
			list.Items[i].SetAPIVersion(r.GVR.GroupVersion().String())
		}
	}
	return list.Items, nil
}
