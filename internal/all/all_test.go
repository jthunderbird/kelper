package all

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func res(group, resource, kind string, namespaced bool, short ...string) Resource {
	return Resource{GVR: schema.GroupVersionResource{Group: group, Version: "v1", Resource: resource}, Kind: kind, Namespaced: namespaced, ShortNames: short}
}

var cluster = []Resource{
	res("apps", "deployments", "Deployment", true, "deploy"),
	res("", "pods", "Pod", true, "po"),
	res("", "nodes", "Node", false, "no"),
	res("", "events", "Event", true, "ev"),
	res("events.k8s.io", "events", "Event", true),
	res("coordination.k8s.io", "leases", "Lease", true),
	res("cert-manager.io", "certificates", "Certificate", true, "cert"),
	res("apiextensions.k8s.io", "customresourcedefinitions", "CustomResourceDefinition", false, "crd"),
}

func names(rs []Resource) string {
	var n []string
	for _, r := range rs {
		n = append(n, r.FullName())
	}
	return strings.Join(n, " ")
}

func TestSelect(t *testing.T) {
	tests := []struct {
		name string
		o    Options
		want string
	}{
		{"everything minus noisy, core first", Options{},
			"nodes pods customresourcedefinitions.apiextensions.k8s.io deployments.apps certificates.cert-manager.io"},
		{"namespace keeps only namespaced types", Options{Namespace: "prod"}, "pods deployments.apps certificates.cert-manager.io"},
		{"noisy on request", Options{Namespace: "x", IncludeNoisy: true}, "events pods deployments.apps certificates.cert-manager.io leases.coordination.k8s.io"},
		{"include by short name beats noisy skip", Options{Include: []string{"ev", "cert"}}, "events certificates.cert-manager.io"},
		{"include by full name and kind", Options{Include: []string{"deployments.apps,Pod"}}, "pods deployments.apps"},
		{"exclude", Options{Namespace: "x", Exclude: []string{"po", "certificates.cert-manager.io"}}, "deployments.apps"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := names(Select(cluster, tt.o)); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestSelectKeepsEventsK8sIoWhenNoCoreEvents(t *testing.T) {
	only := []Resource{res("events.k8s.io", "events", "Event", true)}
	if got := names(Select(only, Options{Include: []string{"events"}})); got != "events.events.k8s.io" {
		t.Errorf("got %q", got)
	}
}

func TestNames(t *testing.T) {
	d := res("apps", "deployments", "Deployment", true)
	if d.FullName() != "deployments.apps" || d.KindName() != "deployment.apps" {
		t.Errorf("%s %s", d.FullName(), d.KindName())
	}
	p := res("", "pods", "Pod", true)
	if p.FullName() != "pods" || p.KindName() != "pod" {
		t.Errorf("%s %s", p.FullName(), p.KindName())
	}
}

type fake struct {
	types  []Resource
	tables map[string]*Table
	fail   map[string]error
	disc   error
}

func (f *fake) Resources(context.Context) ([]Resource, error) { return f.types, f.disc }
func (f *fake) Table(_ context.Context, r Resource, ns, _ string) (*Table, error) {
	if err := f.fail[r.FullName()]; err != nil {
		return nil, err
	}
	return f.tables[r.FullName()], nil
}
func (f *fake) Objects(_ context.Context, r Resource, ns, _ string) ([]unstructured.Unstructured, error) {
	if err := f.fail[r.FullName()]; err != nil {
		return nil, err
	}
	var out []unstructured.Unstructured
	if t := f.tables[r.FullName()]; t != nil {
		for _, row := range t.Rows {
			out = append(out, unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": r.Kind,
				"metadata": map[string]any{"name": row.Name, "namespace": row.Namespace, "uid": "u-" + row.Name},
			}})
		}
	}
	return out, nil
}

func fixture() *fake {
	return &fake{
		types: cluster,
		tables: map[string]*Table{
			"pods": {Columns: []Column{{Name: "Name"}, {Name: "Ready"}, {Name: "IP", Priority: 1}},
				Rows: []Row{{Cells: []string{"web-1", "1/1", "10.0.0.1"}, Name: "web-1", Namespace: "prod"},
					{Cells: []string{"db-0", "0/1", "10.0.0.2"}, Name: "db-0", Namespace: "data"}}},
			"nodes":            {Columns: []Column{{Name: "Name"}, {Name: "Status"}}, Rows: []Row{{Cells: []string{"n1", "Ready"}, Name: "n1"}}},
			"deployments.apps": {Columns: []Column{{Name: "Name"}}},
		},
	}
}

func run(t *testing.T, f *fake, o Options) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := Run(context.Background(), f, o, &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestRunAllNamespacesTables(t *testing.T) {
	out, _, err := run(t, fixture(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Cluster-scoped nodes first (core), pods with a NAMESPACE column, empty types skipped.
	want := "nodes (1)\nNAME   STATUS\nn1     Ready\n\n" +
		"pods (2)\nNAMESPACE   NAME    READY\nprod        web-1   1/1\ndata        db-0    0/1\n"
	if strings.TrimLeft(out, " ") != want && !strings.Contains(out, want) {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	if strings.Contains(out, "deployments") {
		t.Errorf("empty types must be skipped:\n%s", out)
	}
}

func TestRunNamespacedWide(t *testing.T) {
	out, _, err := run(t, fixture(), Options{Namespace: "prod", Output: "wide"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "NAMESPACE") || strings.Contains(out, "nodes") {
		t.Errorf("namespaced run must not show a namespace column or cluster types:\n%s", out)
	}
	if !strings.Contains(out, "IP") || !strings.Contains(out, "10.0.0.1") {
		t.Errorf("wide shows priority columns:\n%s", out)
	}
}

func TestRunNothingFound(t *testing.T) {
	f := &fake{types: cluster, tables: map[string]*Table{}}
	out, errOut, err := run(t, f, Options{Namespace: "empty"})
	if err != nil || out != "" || errOut != "No resources found in empty namespace.\n" {
		t.Errorf("%v %q %q", err, out, errOut)
	}
}

func TestRunNames(t *testing.T) {
	out, _, _ := run(t, fixture(), Options{Output: "name"})
	want := "node/n1\nprod/pod/web-1\ndata/pod/db-0\n"
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
	out, _, _ = run(t, fixture(), Options{Namespace: "prod", Output: "name"})
	if out != "pod/web-1\npod/db-0\n" {
		t.Errorf("namespaced names carry no prefix: %q", out)
	}
}

func TestRunYAMLAndJSON(t *testing.T) {
	out, _, err := run(t, fixture(), Options{Namespace: "prod", Output: "yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "kind: List") || !strings.Contains(out, "name: web-1") || strings.Contains(out, "uid:") {
		t.Errorf("yaml should be a cleaned List:\n%s", out)
	}
	out, _, _ = run(t, fixture(), Options{Namespace: "prod", Output: "json"})
	if !strings.Contains(out, `"kind": "List"`) || !strings.Contains(out, `"uid": "u-web-1"`) {
		t.Errorf("json keeps the object as served:\n%s", out)
	}
}

func TestRunSkipsFailuresButNotAll(t *testing.T) {
	f := fixture()
	f.fail = map[string]error{"nodes": errors.New("nodes is forbidden: User cannot list\nsecond line")}
	out, errOut, err := run(t, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pods (2)") || !strings.HasPrefix(errOut, "Skipped: nodes (nodes is forbidden: User cannot list)") {
		t.Errorf("out=%q err=%q", out, errOut)
	}

	f.fail = map[string]error{}
	for _, r := range Select(cluster, Options{Namespace: "x"}) {
		f.fail[r.FullName()] = errors.New("boom")
	}
	if _, _, err := run(t, f, Options{Namespace: "x"}); err == nil || !strings.Contains(err.Error(), "could not list any") {
		t.Errorf("want failure when nothing lists, got %v", err)
	}
}

func TestRunDiscoveryProblems(t *testing.T) {
	f := fixture()
	f.disc = errors.New("metrics.k8s.io/v1beta1: the server is currently unable to handle the request")
	_, errOut, err := run(t, f, Options{})
	if err != nil || !strings.Contains(errOut, "Degraded discovery:") {
		t.Errorf("partial discovery is reported, not fatal: %v %q", err, errOut)
	}
	if _, _, err := run(t, &fake{disc: errors.New("down")}, Options{}); err == nil || !strings.Contains(err.Error(), "discovery failed") {
		t.Errorf("got %v", err)
	}
	if _, _, err := run(t, &fake{}, Options{}); err == nil {
		t.Error("no types is an error")
	}
}

func TestParseTable(t *testing.T) {
	raw := `{"kind":"Table","columnDefinitions":[{"name":"Name","priority":0},{"name":"Age","priority":0},{"name":"Node","priority":1}],
	 "rows":[{"cells":["web-1",null,42],"object":{"metadata":{"name":"web-1","namespace":"prod"}}}]}`
	tbl, err := ParseTable([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	row := tbl.Rows[0]
	if tbl.Columns[2].Priority != 1 || row.Name != "web-1" || row.Namespace != "prod" || row.Cells[1] != "" || row.Cells[2] != "42" {
		t.Errorf("%+v %+v", tbl.Columns, row)
	}

	list := `{"kind":"PodMetricsList","items":[{"metadata":{"name":"m1","namespace":"kube-system"}}]}`
	tbl, err = ParseTable([]byte(list))
	if err != nil || len(tbl.Rows) != 1 || tbl.Columns[0].Name != "Name" || tbl.Rows[0].Namespace != "kube-system" {
		t.Errorf("list fallback: %v %+v", err, tbl)
	}
	if _, err := ParseTable([]byte("not json")); err == nil {
		t.Error("garbage must fail")
	}
}

func TestConfirmAll(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, "YES\n": true, "n\n": false, "\n": false, "": false} {
		var errOut bytes.Buffer
		got, err := confirmAll(strings.NewReader(in), &errOut)
		if err != nil || got != want {
			t.Errorf("input %q: got %v, %v", in, got, err)
		}
		if !strings.Contains(errOut.String(), "very large amount of output") || !strings.Contains(errOut.String(), "Continue? [y/N]") {
			t.Errorf("warning missing: %q", errOut.String())
		}
	}
}
