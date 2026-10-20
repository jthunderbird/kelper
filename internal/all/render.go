package all

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/jthunderbird/kelper/internal/neat"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// Render writes the results in the requested form. With no namespace in o,
// namespaced types get a NAMESPACE column and -o name lines a namespace prefix.
// bold styles the per-type headings.
func Render(w io.Writer, results []result, o Options, bold func(string) string) error {
	switch o.Output {
	case "yaml", "json":
		return renderList(w, results, o.Output)
	case "name":
		renderNames(w, results, o.Namespace == "")
		return nil
	}
	renderTables(w, results, o, bold)
	return nil
}

func renderTables(w io.Writer, results []result, o Options, bold func(string) string) {
	first := true
	for _, r := range results {
		if r.err != nil || r.table == nil || len(r.table.Rows) == 0 {
			continue
		}
		if !first {
			fmt.Fprintln(w)
		}
		first = false
		fmt.Fprintln(w, bold(fmt.Sprintf("%s (%d)", r.res.FullName(), len(r.table.Rows))))

		showNS := o.Namespace == "" && r.res.Namespaced
		var keep []int
		for i, c := range r.table.Columns {
			if c.Priority == 0 || o.Output == "wide" {
				keep = append(keep, i)
			}
		}
		tw := tabwriter.NewWriter(w, 6, 4, 3, ' ', 0)
		var head []string
		if showNS {
			head = append(head, "NAMESPACE")
		}
		for _, i := range keep {
			head = append(head, strings.ToUpper(r.table.Columns[i].Name))
		}
		fmt.Fprintln(tw, strings.Join(head, "\t"))
		for _, row := range r.table.Rows {
			var cells []string
			if showNS {
				cells = append(cells, row.Namespace)
			}
			for _, i := range keep {
				cell := ""
				if i < len(row.Cells) {
					cell = row.Cells[i]
				}
				cells = append(cells, cell)
			}
			fmt.Fprintln(tw, strings.Join(cells, "\t"))
		}
		tw.Flush()
	}
}

func renderNames(w io.Writer, results []result, allNamespaces bool) {
	for _, r := range results {
		if r.err != nil || r.table == nil {
			continue
		}
		for _, row := range r.table.Rows {
			prefix := ""
			if allNamespaces && r.res.Namespaced && row.Namespace != "" {
				prefix = row.Namespace + "/"
			}
			fmt.Fprintf(w, "%s%s/%s\n", prefix, r.res.KindName(), row.Name)
		}
	}
}

// renderList writes every object as one kind: List. YAML is cleaned like
// `kelper get -o yaml`; Secret values are left as the API returns them.
func renderList(w io.Writer, results []result, format string) error {
	items := []any{}
	for _, r := range results {
		if r.err != nil {
			continue
		}
		for _, obj := range r.objs {
			// kubectl hides managed fields unless asked; so does kelper.
			unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
			items = append(items, obj.Object)
		}
	}
	list := map[string]any{"apiVersion": "v1", "kind": "List", "items": items}
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	if format == "json" {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "    ")
		if err := enc.Encode(list); err != nil {
			return err
		}
		_, err = w.Write(buf.Bytes())
		return err
	}
	y, err := yaml.JSONToYAML(data)
	if err != nil {
		return err
	}
	cleaned, err := neat.Clean(y)
	if err != nil {
		return err
	}
	_, err = w.Write(cleaned)
	return err
}
