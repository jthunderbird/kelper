package get

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jthunderbird/kelper/internal/decode"
	"github.com/jthunderbird/kelper/internal/neat"
	"github.com/jthunderbird/kelper/internal/output"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// Run intercepts `get -o yaml` and routes to decode (Secrets) or neat (everything else).
// args is the os.Args slice starting from "get".
func Run(cs *kubernetes.Clientset, args []string) error {
	cmd := exec.Command("kubectl", args...)
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("kubectl: %w", err)
	}

	var probe struct {
		Kind string `json:"kind"`
	}
	_ = yaml.Unmarshal(raw, &probe)

	isSecret := probe.Kind == "Secret" ||
		(probe.Kind == "List" && strings.Contains(string(raw), "kind: Secret"))

	if isSecret {
		return decode.Print(os.Stdout, raw)
	}

	cleaned, err := neat.Clean(raw)
	if err != nil {
		output.Errorf(os.Stderr, "neat clean failed: %s", err)
		fmt.Print(string(raw))
		return nil
	}
	fmt.Print(string(cleaned))
	return nil
}
