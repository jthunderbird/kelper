package all

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

// pagedServer serves 5 pods two at a time, as a table or a plain list
// depending on the Accept header, and records each request's query.
func pagedServer(t *testing.T, queries *[]string) *rest.Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		*queries = append(*queries, req.URL.RawQuery)
		start := 0
		if c := req.URL.Query().Get("continue"); c != "" {
			fmt.Sscanf(c, "t%d", &start)
		}
		end := start + 2
		next := ""
		if end < 5 {
			next = fmt.Sprintf("t%d", end)
		} else {
			end = 5
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Header.Get("Accept"), "as=Table") {
			rows := ""
			for i := start; i < end; i++ {
				if rows != "" {
					rows += ","
				}
				rows += fmt.Sprintf(`{"cells":["pod-%d"],"object":{"metadata":{"name":"pod-%d","namespace":"ns"}}}`, i, i)
			}
			fmt.Fprintf(w, `{"kind":"Table","metadata":{"continue":%q},"columnDefinitions":[{"name":"Name","priority":0}],"rows":[%s]}`, next, rows)
			return
		}
		items := ""
		for i := start; i < end; i++ {
			if items != "" {
				items += ","
			}
			items += fmt.Sprintf(`{"metadata":{"name":"pod-%d","namespace":"ns"}}`, i)
		}
		fmt.Fprintf(w, `{"kind":"PodList","apiVersion":"v1","metadata":{"continue":%q},"items":[%s]}`, next, items)
	}))
	t.Cleanup(srv.Close)
	return &rest.Config{Host: srv.URL}
}

func TestTableFollowsContinueToken(t *testing.T) {
	var queries []string
	src, err := NewAPISource(pagedServer(t, &queries))
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := src.Table(context.Background(), res("", "pods", "Pod", true), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Rows) != 5 || tbl.Rows[4].Name != "pod-4" {
		t.Fatalf("rows = %+v", tbl.Rows)
	}
	if len(queries) != 3 {
		t.Fatalf("requests = %v, want 3 pages", queries)
	}
	for _, q := range queries {
		if !strings.Contains(q, "limit=500") {
			t.Errorf("request %q lacks limit=500", q)
		}
	}
	if !strings.Contains(queries[1], "continue=t2") || !strings.Contains(queries[2], "continue=t4") {
		t.Errorf("continue tokens not followed: %v", queries)
	}
}

func TestObjectsFollowsContinueToken(t *testing.T) {
	var queries []string
	src, err := NewAPISource(pagedServer(t, &queries))
	if err != nil {
		t.Fatal(err)
	}
	objs, err := src.Objects(context.Background(), res("", "pods", "Pod", true), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 5 || objs[4].GetName() != "pod-4" || objs[0].GetKind() != "Pod" {
		t.Fatalf("objects = %d, first kind %q", len(objs), objs[0].GetKind())
	}
	if len(queries) != 3 {
		t.Fatalf("requests = %v, want 3 pages", queries)
	}
}
