package decode

import (
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"github.com/jthunderbird/kelper/internal/output"
	"sigs.k8s.io/yaml"
)

type secret struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Data map[string]string `json:"data"`
}

type secretList struct {
	Kind  string   `json:"kind"`
	Items []secret `json:"items"`
}

// Print decodes and prints secret data values from yamlBytes to w.
// Handles both kind: Secret and kind: List of Secrets.
func Print(w io.Writer, yamlBytes []byte) error {
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := yaml.Unmarshal(yamlBytes, &probe); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}

	switch probe.Kind {
	case "Secret":
		var s secret
		if err := yaml.Unmarshal(yamlBytes, &s); err != nil {
			return fmt.Errorf("parse secret: %w", err)
		}
		printSecret(w, s)
	case "List":
		var sl secretList
		if err := yaml.Unmarshal(yamlBytes, &sl); err != nil {
			return fmt.Errorf("parse secret list: %w", err)
		}
		for _, s := range sl.Items {
			if s.Kind == "Secret" {
				printSecret(w, s)
			}
		}
	default:
		return fmt.Errorf("expected kind Secret or List, got %q", probe.Kind)
	}
	return nil
}

func printSecret(w io.Writer, s secret) {
	for key, encoded := range s.Data {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			fmt.Fprintf(w, "%s\n%s\n(decode error: %v)\n\n",
				output.KeyLabel(key), output.Separator(25), err)
			continue
		}
		value := strings.ReplaceAll(string(decoded), `\n`, "\n")
		fmt.Fprintf(w, "%s\n%s\n%s\n\n",
			output.KeyLabel(key), output.Separator(25), value)
	}
}
