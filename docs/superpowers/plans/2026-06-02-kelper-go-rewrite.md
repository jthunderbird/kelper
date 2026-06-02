# Kelper Go Rewrite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite the `kelper` bash kubectl wrapper as a self-contained Go binary with zero external tool dependencies, richer output formatting, and interactive TUI modes for healthcheck and kubeconfig generation.

**Architecture:** cobra root command dispatches to feature packages under `internal/`; all Kubernetes API access goes through `client-go`; bubbletea drives TUI modes; a shared `internal/output` package provides consistent formatting across all commands.

**Tech Stack:** Go 1.21, cobra, client-go, bubbletea, lipgloss, tablewriter, sigs.k8s.io/yaml, kubectl-neat

---

## File Map

| File | Responsibility |
|---|---|
| `cmd/kelper/main.go` | Entrypoint, cobra root, get interception, passthrough dispatch |
| `internal/client/client.go` | Build client-go clientset from kubeconfig |
| `internal/output/output.go` | Tables, pod headers, separator lines, YAML marshal, error printing |
| `internal/passthrough/passthrough.go` | Exec kubectl with inherited stdin/stdout/stderr |
| `internal/get/get.go` | Detect -o yaml, route to neat or decode |
| `internal/neat/neat.go` | YAML field stripping via kubectl-neat |
| `internal/decode/decode.go` | Secret base64 decode, flush-left display |
| `internal/healthcheck/healthcheck.go` | Non-interactive table mode, exit code |
| `internal/healthcheck/tui.go` | bubbletea TUI: namespace selector + live refresh |
| `internal/images/images.go` | Pod image inspector |
| `internal/resources/resources.go` | Pod resource limits/requests inspector |
| `internal/volumes/volumes.go` | Pod volumeMount + volumes inspector |
| `internal/kubeconfig/kubeconfig.go` | CSR, RBAC creation, kubeconfig emit |
| `internal/kubeconfig/tui.go` | bubbletea adaptive wizard |

---

## Task 1: Repository Cleanup & Module Reset

**Files:**
- Delete: `cmd/kelp/main.go`
- Delete: `kelp` (compiled binary)
- Delete: `main` (compiled binary)
- Rename: `kelper` → `kelper.sh`
- Modify: `go.mod`

- [ ] **Step 1: Remove old Go prototype and binaries**

```bash
rm -rf cmd/kelp
rm -f kelp main
```

- [ ] **Step 2: Rename bash script**

```bash
mv kelper kelper.sh
```

- [ ] **Step 3: Create new module and directory structure**

```bash
mkdir -p cmd/kelper internal/client internal/output internal/passthrough \
  internal/get internal/neat internal/decode \
  internal/healthcheck internal/images internal/resources internal/volumes \
  internal/kubeconfig
```

- [ ] **Step 4: Write new go.mod**

```
module github.com/jthunderbird/kelper

go 1.21
```

Write this to `go.mod` (overwrite existing).

- [ ] **Step 5: Initialize dependencies**

```bash
go get github.com/spf13/cobra@latest
go get k8s.io/client-go@latest
go get k8s.io/api@latest
go get k8s.io/apimachinery@latest
go get sigs.k8s.io/yaml@latest
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/lipgloss@latest
go get github.com/olekukonko/tablewriter@latest
go get sigs.k8s.io/kubectl-neat@latest
go mod tidy
```

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "chore: remove old Go prototype, rename bash script, reset go.mod"
```

---

## Task 2: `internal/client` — Kubernetes Client Setup

**Files:**
- Create: `internal/client/client.go`

- [ ] **Step 1: Write the test**

Create `internal/client/client_test.go`:

```go
package client_test

import (
	"testing"

	"github.com/jthunderbird/kelper/internal/client"
)

func TestNewClientReturnsErrorWithBadKubeconfig(t *testing.T) {
	_, err := client.New("/nonexistent/path/to/kubeconfig")
	if err == nil {
		t.Fatal("expected error for nonexistent kubeconfig, got nil")
	}
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
go test ./internal/client/... -v
```

Expected: compile error — package does not exist yet.

- [ ] **Step 3: Implement client.go**

```go
package client

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// New builds a Kubernetes clientset. If kubeconfigPath is empty, it falls back
// to the default kubeconfig discovery order (KUBECONFIG env, ~/.kube/config,
// in-cluster service account).
func New(kubeconfigPath string) (*kubernetes.Clientset, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}
```

- [ ] **Step 4: Run test to confirm it passes**

```bash
go test ./internal/client/... -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/client/
git commit -m "feat: add internal/client package for client-go setup"
```

---

## Task 3: `internal/output` — Shared Formatting

**Files:**
- Create: `internal/output/output.go`

- [ ] **Step 1: Write the tests**

Create `internal/output/output_test.go`:

```go
package output_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/output"
)

func TestSeparatorLineLength(t *testing.T) {
	sep := output.Separator(20)
	// strip ANSI codes for length check
	plain := strings.ReplaceAll(sep, "\x1b[2m", "")
	plain = strings.ReplaceAll(plain, "\x1b[0m", "")
	if len([]rune(plain)) != 20 {
		t.Errorf("expected separator of 20 runes, got %d", len([]rune(plain)))
	}
}

func TestPodHeaderContainsPodName(t *testing.T) {
	var buf bytes.Buffer
	output.PodHeader(&buf, "mypod", "mynamespace")
	out := buf.String()
	if !strings.Contains(out, "mypod") {
		t.Errorf("pod header missing pod name, got: %s", out)
	}
	if !strings.Contains(out, "mynamespace") {
		t.Errorf("pod header missing namespace, got: %s", out)
	}
}

func TestErrorfWritesToWriter(t *testing.T) {
	var buf bytes.Buffer
	output.Errorf(&buf, "something went wrong: %s", "details")
	out := buf.String()
	if !strings.Contains(out, "error:") {
		t.Errorf("expected 'error:' prefix, got: %s", out)
	}
	if !strings.Contains(out, "something went wrong: details") {
		t.Errorf("expected error message in output, got: %s", out)
	}
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
go test ./internal/output/... -v
```

Expected: compile error — package does not exist yet.

- [ ] **Step 3: Implement output.go**

```go
package output

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	podHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("33"))
	keyLabelStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("36"))
	dimStyle       = lipgloss.NewStyle().Faint(true)
	errorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// Separator returns a unicode rule line of n runes in a dim style.
func Separator(n int) string {
	return dimStyle.Render(strings.Repeat("─", n))
}

// PodHeader writes a colored pod name + namespace header followed by a separator.
func PodHeader(w io.Writer, podName, namespace string) {
	header := fmt.Sprintf("pod: %s (-n %s)", podName, namespace)
	fmt.Fprintln(w, podHeaderStyle.Render(header))
	fmt.Fprintln(w, Separator(len(header)))
	fmt.Fprintln(w)
}

// KeyLabel returns a highlighted key label for secret output.
func KeyLabel(key string) string {
	return keyLabelStyle.Render("KEY: " + key)
}

// Errorf writes a formatted error message with "error: " prefix to w.
func Errorf(w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(w, errorStyle.Render("error: "+msg))
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
go test ./internal/output/... -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/output/
git commit -m "feat: add internal/output shared formatting package"
```

---

## Task 4: `internal/passthrough` — kubectl Transparent Forwarding

**Files:**
- Create: `internal/passthrough/passthrough.go`

- [ ] **Step 1: Write the test**

Create `internal/passthrough/passthrough_test.go`:

```go
package passthrough_test

import (
	"testing"

	"github.com/jthunderbird/kelper/internal/passthrough"
)

func TestRunReturnsErrorWhenKubectlMissing(t *testing.T) {
	// Override kubectl binary to something that doesn't exist.
	err := passthrough.RunWith("/nonexistent/binary", []string{"get", "pods"})
	if err == nil {
		t.Fatal("expected error when binary does not exist, got nil")
	}
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
go test ./internal/passthrough/... -v
```

Expected: compile error.

- [ ] **Step 3: Implement passthrough.go**

```go
package passthrough

import (
	"os"
	"os/exec"
)

// Run execs kubectl with the given args, inheriting stdin/stdout/stderr and
// the caller's exit code. This is a process replacement — it does not return
// on success; the process exits with kubectl's exit code.
func Run(args []string) error {
	return RunWith("kubectl", args)
}

// RunWith execs the named binary with args, inheriting stdin/stdout/stderr.
// Exposed for testing with an alternate binary path.
func RunWith(binary string, args []string) error {
	cmd := exec.Command(binary, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
go test ./internal/passthrough/... -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/passthrough/
git commit -m "feat: add internal/passthrough for transparent kubectl forwarding"
```

---

## Task 5: `cmd/kelper/main.go` — Root Command & Get Interception

**Files:**
- Create: `cmd/kelper/main.go`

This is the entrypoint. It wires all cobra commands and implements the `get -o yaml` interception check. Non-feature subcommands pass through to `passthrough.Run`.

- [ ] **Step 1: Write the entrypoint**

```go
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/jthunderbird/kelper/internal/client"
	"github.com/jthunderbird/kelper/internal/decode"
	"github.com/jthunderbird/kelper/internal/get"
	"github.com/jthunderbird/kelper/internal/healthcheck"
	"github.com/jthunderbird/kelper/internal/images"
	"github.com/jthunderbird/kelper/internal/kubeconfig"
	"github.com/jthunderbird/kelper/internal/output"
	"github.com/jthunderbird/kelper/internal/passthrough"
	"github.com/jthunderbird/kelper/internal/resources"
	"github.com/jthunderbird/kelper/internal/volumes"

	"k8s.io/client-go/kubernetes"
)

func main() {
	var kubeconfigPath string
	var cs *kubernetes.Clientset

	root := &cobra.Command{
		Use:   "kelper",
		Short: "kubectl wrapper with enhanced output and interactivity",
		// DisableFlagParsing allows unknown subcommands to be captured and
		// forwarded to kubectl transparently.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// If no args, print help.
			if len(args) == 0 {
				return cmd.Help()
			}
			// Intercept: get -o yaml (and not --raw)
			if args[0] == "get" && isYAMLOutput(args) && !hasFlag(args, "--raw") {
				return get.Run(cs, args)
			}
			// Everything else: transparent passthrough.
			return passthrough.Run(args)
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip client init for passthrough and completion.
			if cmd.Name() == "completion" {
				return nil
			}
			var err error
			cs, err = client.New(kubeconfigPath)
			if err != nil {
				output.Errorf(os.Stderr, "could not connect to cluster: %s", err)
				os.Exit(1)
			}
			return nil
		},
	}

	root.PersistentFlags().StringVar(&kubeconfigPath, "kubeconfig", "", "path to kubeconfig file (default: $KUBECONFIG or ~/.kube/config)")

	// Register feature subcommands.
	root.AddCommand(healthcheck.Command(&cs))
	root.AddCommand(images.Command(&cs))
	root.AddCommand(resources.Command(&cs))
	root.AddCommand(volumes.Command(&cs))
	root.AddCommand(kubeconfig.Command())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// isYAMLOutput returns true if args contain -o yaml or -oyaml.
func isYAMLOutput(args []string) bool {
	joined := strings.Join(args, " ")
	return strings.Contains(joined, "-o yaml") || strings.Contains(joined, "-oyaml")
}

// hasFlag returns true if args contain the given flag string.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Verify it compiles (feature packages are stubs at this point — add minimal stubs)**

Each feature package needs a minimal exported function to compile. Add these stubs now (they will be replaced in later tasks):

`internal/get/get.go`:
```go
package get

import (
	"fmt"
	"k8s.io/client-go/kubernetes"
)

func Run(cs *kubernetes.Clientset, args []string) error {
	fmt.Println("get: not yet implemented")
	return nil
}
```

`internal/healthcheck/healthcheck.go`:
```go
package healthcheck

import (
	"fmt"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

func Command(cs **kubernetes.Clientset) *cobra.Command {
	return &cobra.Command{
		Use:     "healthcheck",
		Aliases: []string{"health"},
		Short:   "Check cluster health",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("healthcheck: not yet implemented")
			return nil
		},
	}
}
```

`internal/images/images.go`:
```go
package images

import (
	"fmt"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

func Command(cs **kubernetes.Clientset) *cobra.Command {
	return &cobra.Command{
		Use:     "images",
		Aliases: []string{"image", "imgs", "img"},
		Short:   "Show container images per pod",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("images: not yet implemented")
			return nil
		},
	}
}
```

`internal/resources/resources.go`:
```go
package resources

import (
	"fmt"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

func Command(cs **kubernetes.Clientset) *cobra.Command {
	return &cobra.Command{
		Use:     "resources",
		Aliases: []string{"resource", "res"},
		Short:   "Show resource limits and requests per pod",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("resources: not yet implemented")
			return nil
		},
	}
}
```

`internal/volumes/volumes.go`:
```go
package volumes

import (
	"fmt"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

func Command(cs **kubernetes.Clientset) *cobra.Command {
	return &cobra.Command{
		Use:     "volumes",
		Aliases: []string{"volume", "vols", "vol"},
		Short:   "Show volume mounts per pod",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("volumes: not yet implemented")
			return nil
		},
	}
}
```

`internal/kubeconfig/kubeconfig.go`:
```go
package kubeconfig

import (
	"fmt"
	"github.com/spf13/cobra"
)

func Command() *cobra.Command {
	return &cobra.Command{
		Use:   "kubeconfig",
		Short: "Generate kubeconfig files",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("kubeconfig: not yet implemented")
			return nil
		},
	}
}
```

`internal/decode/decode.go`:
```go
package decode

import (
	"fmt"
	"k8s.io/client-go/kubernetes"
)

func Run(cs *kubernetes.Clientset, args []string) error {
	fmt.Println("decode: not yet implemented")
	return nil
}
```

- [ ] **Step 3: Build and confirm it compiles**

```bash
go build ./cmd/kelper/
```

Expected: binary `kelper` produced with no errors.

- [ ] **Step 4: Smoke test passthrough**

```bash
./kelper version
```

Expected: output identical to `kubectl version`.

- [ ] **Step 5: Commit**

```bash
git add cmd/kelper/ internal/get/ internal/decode/ internal/healthcheck/ \
  internal/images/ internal/resources/ internal/volumes/ internal/kubeconfig/
git commit -m "feat: add cobra root command with passthrough and feature stubs"
```

---

## Task 6: `internal/neat` & `internal/decode` — YAML Transform

**Files:**
- Create: `internal/neat/neat.go`
- Replace stub: `internal/decode/decode.go`
- Replace stub: `internal/get/get.go`

- [ ] **Step 1: Write tests for neat**

Create `internal/neat/neat_test.go`:

```go
package neat_test

import (
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/neat"
)

const deploymentYAML = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: myapp
  namespace: default
  uid: abc-123
  resourceVersion: "12345"
  creationTimestamp: "2024-01-01T00:00:00Z"
  generation: 3
spec:
  progressDeadlineSeconds: 600
  revisionHistoryLimit: 10
  selector:
    matchLabels:
      app: myapp
  template:
    metadata:
      creationTimestamp: null
    spec:
      containers:
      - name: app
        image: myimage:latest
        terminationMessagePath: /dev/termination-log
        terminationMessagePolicy: File
      dnsPolicy: ClusterFirst
      restartPolicy: Always
      schedulerName: default-scheduler
      terminationGracePeriodSeconds: 30
status:
  availableReplicas: 1
`

func TestNeatRemovesUID(t *testing.T) {
	result, err := neat.Clean([]byte(deploymentYAML))
	if err != nil {
		t.Fatalf("Clean returned error: %v", err)
	}
	if strings.Contains(string(result), "uid:") {
		t.Error("neat output should not contain uid")
	}
}

func TestNeatRemovesStatus(t *testing.T) {
	result, err := neat.Clean([]byte(deploymentYAML))
	if err != nil {
		t.Fatalf("Clean returned error: %v", err)
	}
	if strings.Contains(string(result), "status:") {
		t.Error("neat output should not contain status")
	}
}

func TestNeatRemovesResourceVersion(t *testing.T) {
	result, err := neat.Clean([]byte(deploymentYAML))
	if err != nil {
		t.Fatalf("Clean returned error: %v", err)
	}
	if strings.Contains(string(result), "resourceVersion") {
		t.Error("neat output should not contain resourceVersion")
	}
}

func TestNeatPreservesName(t *testing.T) {
	result, err := neat.Clean([]byte(deploymentYAML))
	if err != nil {
		t.Fatalf("Clean returned error: %v", err)
	}
	if !strings.Contains(string(result), "name: myapp") {
		t.Error("neat output should preserve resource name")
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./internal/neat/... -v
```

Expected: compile error.

- [ ] **Step 3: Implement neat.go**

Attempt to use kubectl-neat as a library first. If the import path `sigs.k8s.io/kubectl-neat/pkg/clean` is available after `go get`, use it:

```go
package neat

import (
	"fmt"

	"sigs.k8s.io/kubectl-neat/pkg/clean"
	"sigs.k8s.io/yaml"
)

// Clean strips server-populated default fields from Kubernetes YAML.
// It handles both single resources and kind: List.
func Clean(yamlBytes []byte) ([]byte, error) {
	// kubectl-neat works on JSON internally; convert via sigs.k8s.io/yaml.
	jsonBytes, err := yaml.YAMLToJSON(yamlBytes)
	if err != nil {
		return nil, fmt.Errorf("yaml to json: %w", err)
	}
	cleaned, err := clean.Resource(jsonBytes)
	if err != nil {
		return nil, fmt.Errorf("neat clean: %w", err)
	}
	return yaml.JSONToYAML(cleaned)
}
```

If `sigs.k8s.io/kubectl-neat/pkg/clean` is not importable (check with `go build`), fall back to the denylist implementation below instead:

```go
package neat

import (
	"sigs.k8s.io/yaml"
)

// genericDenylist fields removed from all resource kinds.
var genericDenylist = [][]string{
	{"metadata", "uid"},
	{"metadata", "resourceVersion"},
	{"metadata", "creationTimestamp"},
	{"metadata", "generation"},
	{"metadata", "ownerReferences"},
	{"metadata", "generateName"},
	{"metadata", "finalizers"},
	{"metadata", "managedFields"},
	{"status"},
}

// kindDenylist fields removed per resource kind (matched on .kind value).
var kindDenylist = map[string][][]string{
	"Deployment": {
		{"spec", "progressDeadlineSeconds"},
		{"spec", "revisionHistoryLimit"},
		{"spec", "template", "metadata", "creationTimestamp"},
		{"spec", "template", "spec", "terminationGracePeriodSeconds"},
		{"spec", "template", "spec", "dnsPolicy"},
		{"spec", "template", "spec", "restartPolicy"},
		{"spec", "template", "spec", "schedulerName"},
	},
	"StatefulSet": {
		{"spec", "template", "metadata", "creationTimestamp"},
		{"spec", "template", "spec", "terminationGracePeriodSeconds"},
		{"spec", "template", "spec", "dnsPolicy"},
		{"spec", "template", "spec", "restartPolicy"},
		{"spec", "template", "spec", "schedulerName"},
	},
	"DaemonSet": {
		{"spec", "template", "metadata", "creationTimestamp"},
		{"spec", "template", "spec", "terminationGracePeriodSeconds"},
		{"spec", "template", "spec", "dnsPolicy"},
		{"spec", "template", "spec", "restartPolicy"},
		{"spec", "template", "spec", "schedulerName"},
	},
	"Pod": {
		{"spec", "terminationGracePeriodSeconds"},
		{"spec", "dnsPolicy"},
		{"spec", "restartPolicy"},
		{"spec", "schedulerName"},
		{"spec", "nodeName"},
		{"spec", "serviceAccountName"},
		{"spec", "enableServiceLinks"},
		{"spec", "preemptionPolicy"},
		{"spec", "priority"},
	},
	"Service": {
		{"spec", "clusterIP"},
		{"spec", "clusterIPs"},
		{"spec", "internalTrafficPolicy"},
		{"spec", "ipFamilies"},
		{"spec", "ipFamilyPolicy"},
		{"spec", "sessionAffinity"},
	},
}

// Clean strips server-populated default fields from Kubernetes YAML.
// Handles both single resources and kind: List.
func Clean(yamlBytes []byte) ([]byte, error) {
	var obj map[string]interface{}
	if err := yaml.Unmarshal(yamlBytes, &obj); err != nil {
		return nil, err
	}

	kind, _ := obj["kind"].(string)
	if kind == "List" {
		return cleanList(obj)
	}
	cleanObject(obj, kind)
	return yaml.Marshal(obj)
}

func cleanList(obj map[string]interface{}) ([]byte, error) {
	items, _ := obj["items"].([]interface{})
	for i, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		kind, _ := m["kind"].(string)
		cleanObject(m, kind)
		items[i] = m
	}
	obj["items"] = items
	return yaml.Marshal(obj)
}

func cleanObject(obj map[string]interface{}, kind string) {
	for _, path := range genericDenylist {
		deletePath(obj, path)
	}
	for _, path := range kindDenylist[kind] {
		deletePath(obj, path)
	}
	// Strip terminationMessagePath and terminationMessagePolicy from all containers.
	stripContainerFields(obj, "spec", "containers")
	stripContainerFields(obj, "spec", "initContainers")
	stripContainerFields(obj, "spec", "template", "spec", "containers")
	stripContainerFields(obj, "spec", "template", "spec", "initContainers")
}

func deletePath(obj map[string]interface{}, path []string) {
	if len(path) == 1 {
		delete(obj, path[0])
		return
	}
	next, ok := obj[path[0]].(map[string]interface{})
	if !ok {
		return
	}
	deletePath(next, path[1:])
}

func stripContainerFields(obj map[string]interface{}, path ...string) {
	cur := obj
	for _, key := range path[:len(path)-1] {
		next, ok := cur[key].(map[string]interface{})
		if !ok {
			return
		}
		cur = next
	}
	containers, ok := cur[path[len(path)-1]].([]interface{})
	if !ok {
		return
	}
	for _, c := range containers {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		delete(m, "terminationMessagePath")
		delete(m, "terminationMessagePolicy")
	}
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
go test ./internal/neat/... -v
```

Expected: PASS

- [ ] **Step 5: Write tests for decode**

Create `internal/decode/decode_test.go`:

```go
package decode_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/decode"
)

const secretYAML = `
apiVersion: v1
kind: Secret
metadata:
  name: my-secret
  namespace: default
data:
  username: YWRtaW4=
  password: cHJvbS1vcGVyYXRvcg==
`

const secretListYAML = `
apiVersion: v1
kind: List
items:
- apiVersion: v1
  kind: Secret
  metadata:
    name: secret-one
    namespace: default
  data:
    key: dmFsdWU=
`

func TestDecodeSecretValues(t *testing.T) {
	var buf bytes.Buffer
	if err := decode.Print(&buf, []byte(secretYAML)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "admin") {
		t.Errorf("expected decoded username 'admin' in output, got: %s", out)
	}
	if !strings.Contains(out, "prom-operator") {
		t.Errorf("expected decoded password 'prom-operator' in output, got: %s", out)
	}
}

func TestDecodeSecretKeyLabels(t *testing.T) {
	var buf bytes.Buffer
	if err := decode.Print(&buf, []byte(secretYAML)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "KEY:") {
		t.Errorf("expected KEY: label in output, got: %s", out)
	}
}

func TestDecodeSecretList(t *testing.T) {
	var buf bytes.Buffer
	if err := decode.Print(&buf, []byte(secretListYAML)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "value") {
		t.Errorf("expected decoded value 'value' in output, got: %s", out)
	}
}

func TestDecodeNoBase64PaddingInOutput(t *testing.T) {
	var buf bytes.Buffer
	if err := decode.Print(&buf, []byte(secretYAML)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The raw base64 strings should not appear in output.
	out := buf.String()
	if strings.Contains(out, "YWRtaW4=") {
		t.Errorf("raw base64 should not appear in decoded output")
	}
}
```

- [ ] **Step 6: Run decode tests to confirm they fail**

```bash
go test ./internal/decode/... -v
```

Expected: FAIL — stub returns not implemented.

- [ ] **Step 7: Implement decode.go**

Replace `internal/decode/decode.go`:

```go
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
	Kind     string            `json:"kind"`
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
	// Unmarshal into a generic map to check kind.
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
			fmt.Fprintf(w, "%s\n%s\n(decode error: %v)\n\n", output.KeyLabel(key), output.Separator(25), err)
			continue
		}
		value := strings.ReplaceAll(string(decoded), `\n`, "\n")
		fmt.Fprintf(w, "%s\n%s\n%s\n\n", output.KeyLabel(key), output.Separator(25), value)
	}
}
```

- [ ] **Step 8: Implement get.go — routes to neat or decode**

Replace `internal/get/get.go`:

```go
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
)

// Run intercepts `get -o yaml` and routes to decode (Secrets) or neat (everything else).
// args is the full os.Args slice starting from "get".
func Run(cs *kubernetes.Clientset, args []string) error {
	// Run kubectl and capture stdout.
	cmd := exec.Command("kubectl", args...)
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("kubectl: %w", err)
	}

	// Detect Secret.
	var probe struct {
		Kind string `json:"kind"`
	}
	_ = unmarshalKind(raw, &probe)

	isSecret := probe.Kind == "Secret" ||
		(probe.Kind == "List" && containsSecrets(raw))

	if isSecret {
		return decode.Print(os.Stdout, raw)
	}

	cleaned, err := neat.Clean(raw)
	if err != nil {
		output.Errorf(os.Stderr, "neat clean failed: %s", err)
		// Fall back to raw output.
		fmt.Print(string(raw))
		return nil
	}
	fmt.Print(string(cleaned))
	return nil
}

func unmarshalKind(data []byte, v interface{}) error {
	// Use sigs.k8s.io/yaml for kind probe.
	import "sigs.k8s.io/yaml"
	return yaml.Unmarshal(data, v)
}

func containsSecrets(data []byte) bool {
	return strings.Contains(string(data), "kind: Secret")
}
```

Note: the inline import in `unmarshalKind` is illustrative — move it to the file-level imports block. The actual file-level import for `sigs.k8s.io/yaml` must be added to the import block at the top of get.go. Rewrite get.go cleanly:

```go
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
```

- [ ] **Step 9: Run all tests**

```bash
go test ./internal/neat/... ./internal/decode/... -v
```

Expected: all PASS

- [ ] **Step 10: Build and verify**

```bash
go build ./cmd/kelper/
```

Expected: no errors.

- [ ] **Step 11: Commit**

```bash
git add internal/neat/ internal/decode/ internal/get/
git commit -m "feat: implement neat YAML stripping and secret decode"
```

---

## Task 7: `internal/healthcheck` — Non-Interactive Table Mode

**Files:**
- Replace stub: `internal/healthcheck/healthcheck.go`

- [ ] **Step 1: Write the tests**

Create `internal/healthcheck/healthcheck_test.go`:

```go
package healthcheck_test

import (
	"testing"

	"github.com/jthunderbird/kelper/internal/healthcheck"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsUnhealthyPodDetectsCrashLoop(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-pod", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: false, State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				}},
			},
		},
	}
	if !healthcheck.IsUnhealthyPod(pod) {
		t.Error("expected CrashLoopBackOff pod to be unhealthy")
	}
}

func TestIsUnhealthyPodIgnoresCompleted(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "done-pod", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodSucceeded,
		},
	}
	if healthcheck.IsUnhealthyPod(pod) {
		t.Error("expected Succeeded pod to be considered healthy")
	}
}

func TestIsUnhealthyPodAllContainersReady(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "good-pod", Namespace: "default"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true},
				{Ready: true},
			},
		},
	}
	if healthcheck.IsUnhealthyPod(pod) {
		t.Error("expected all-ready pod to be healthy")
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./internal/healthcheck/... -v
```

Expected: compile error — IsUnhealthyPod not defined.

- [ ] **Step 3: Implement healthcheck.go**

Replace `internal/healthcheck/healthcheck.go`:

```go
package healthcheck

import (
	"context"
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/jthunderbird/kelper/internal/output"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))

// Command returns the cobra command for healthcheck / health.
func Command(cs **kubernetes.Clientset) *cobra.Command {
	var namespace string
	var allNamespaces bool

	cmd := &cobra.Command{
		Use:     "healthcheck",
		Aliases: []string{"health"},
		Short:   "Check cluster health",
		RunE: func(cmd *cobra.Command, args []string) error {
			// No targeting flags → TUI mode (implemented in Task 8).
			if !allNamespaces && namespace == "" {
				return runTUI(*cs)
			}
			ns := namespace
			if allNamespaces {
				ns = ""
			}
			return RunTable(*cs, ns)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace to check")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "check all namespaces")
	return cmd
}

// RunTable runs a non-interactive healthcheck and prints tables to stdout.
// Returns an error if any unhealthy resources are found (non-zero exit).
func RunTable(cs *kubernetes.Clientset, namespace string) error {
	ctx := context.Background()

	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		output.Errorf(os.Stderr, "list pods: %s", err)
		os.Exit(1)
	}

	var unhealthyPods []corev1.Pod
	for _, p := range pods.Items {
		if IsUnhealthyPod(p) {
			unhealthyPods = append(unhealthyPods, p)
		}
	}

	unhealthyWorkloads, err := listUnhealthyWorkloads(ctx, cs, namespace)
	if err != nil {
		output.Errorf(os.Stderr, "list workloads: %s", err)
		os.Exit(1)
	}

	// Print unhealthy pods table.
	fmt.Println(sectionStyle.Render("Unhealthy Pods"))
	if len(unhealthyPods) == 0 {
		fmt.Println("  (none)")
	} else {
		t := tablewriter.NewWriter(os.Stdout)
		t.SetHeader([]string{"NAMESPACE", "NAME", "READY", "STATUS"})
		t.SetBorder(false)
		t.SetColumnSeparator("  ")
		for _, p := range unhealthyPods {
			ready := podReadyString(p)
			status := podStatusString(p)
			t.Append([]string{p.Namespace, p.Name, ready, status})
		}
		t.Render()
	}
	fmt.Println()

	// Print unhealthy workloads table.
	fmt.Println(sectionStyle.Render("Unhealthy Workloads"))
	if len(unhealthyWorkloads) == 0 {
		fmt.Println("  (none)")
	} else {
		t := tablewriter.NewWriter(os.Stdout)
		t.SetHeader([]string{"NAMESPACE", "KIND", "NAME", "AVAILABLE", "DESIRED"})
		t.SetBorder(false)
		t.SetColumnSeparator("  ")
		for _, w := range unhealthyWorkloads {
			t.Append([]string{w.Namespace, w.Kind, w.Name, fmt.Sprint(w.Available), fmt.Sprint(w.Desired)})
		}
		t.Render()
	}
	fmt.Println()

	// Summary.
	fmt.Printf("Summary: %d unhealthy pod(s), %d unhealthy workload(s)\n",
		len(unhealthyPods), len(unhealthyWorkloads))

	if len(unhealthyPods) > 0 || len(unhealthyWorkloads) > 0 {
		return fmt.Errorf("unhealthy resources found")
	}
	return nil
}

// IsUnhealthyPod returns true if a pod is not in a healthy terminal or running state.
func IsUnhealthyPod(p corev1.Pod) bool {
	// Completed jobs are healthy by design.
	if p.Status.Phase == corev1.PodSucceeded {
		return false
	}
	// All containers must be ready.
	for _, cs := range p.Status.ContainerStatuses {
		if !cs.Ready {
			return true
		}
	}
	return false
}

type workloadStatus struct {
	Namespace string
	Kind      string
	Name      string
	Available int32
	Desired   int32
}

func listUnhealthyWorkloads(ctx context.Context, cs *kubernetes.Clientset, namespace string) ([]workloadStatus, error) {
	var results []workloadStatus

	// Deployments.
	deploys, err := cs.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, d := range deploys.Items {
		desired := *d.Spec.Replicas
		available := d.Status.AvailableReplicas
		if available < desired {
			results = append(results, workloadStatus{d.Namespace, "Deployment", d.Name, available, desired})
		}
	}

	// StatefulSets.
	stss, err := cs.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, s := range stss.Items {
		desired := *s.Spec.Replicas
		ready := s.Status.ReadyReplicas
		if ready < desired {
			results = append(results, workloadStatus{s.Namespace, "StatefulSet", s.Name, ready, desired})
		}
	}

	// DaemonSets.
	dss, err := cs.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, d := range dss.Items {
		desired := d.Status.DesiredNumberScheduled
		ready := d.Status.NumberReady
		if ready < desired {
			results = append(results, workloadStatus{d.Namespace, "DaemonSet", d.Name, ready, desired})
		}
	}

	// Jobs.
	jobs, err := cs.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, j := range jobs.Items {
		for _, c := range j.Status.Conditions {
			if c.Type == "Failed" && c.Status == "True" {
				results = append(results, workloadStatus{j.Namespace, "Job", j.Name, 0, 1})
			}
		}
	}

	// CronJobs — check last job status.
	cjs, err := cs.BatchV1().CronJobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, cj := range cjs.Items {
		if cj.Status.LastScheduleTime != nil && len(cj.Status.Active) == 0 {
			// Only flag if the last schedule produced no active jobs and
			// there is a failed condition on a child job — covered by Job
			// loop above. CronJob itself is healthy if it has scheduled.
			_ = cj
		}
	}

	return results, nil
}

func podReadyString(p corev1.Pod) string {
	total := len(p.Status.ContainerStatuses)
	ready := 0
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, total)
}

func podStatusString(p corev1.Pod) string {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			return cs.State.Waiting.Reason
		}
	}
	return string(p.Status.Phase)
}

// runTUI is the placeholder called when no targeting flags are given.
// Implemented in tui.go (Task 8).
func runTUI(cs *kubernetes.Clientset) error {
	return RunTable(cs, "default")
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/healthcheck/... -v
```

Expected: PASS

- [ ] **Step 5: Build**

```bash
go build ./cmd/kelper/
```

Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add internal/healthcheck/healthcheck.go
git commit -m "feat: implement healthcheck non-interactive table mode"
```

---

## Task 8: `internal/healthcheck/tui.go` — Interactive TUI

**Files:**
- Create: `internal/healthcheck/tui.go`

- [ ] **Step 1: Write a smoke test for the TUI model initialization**

Add to `internal/healthcheck/healthcheck_test.go`:

```go
func TestNewTUIModelHasNamespaces(t *testing.T) {
	namespaces := []string{"default", "kube-system", "monitoring"}
	m := healthcheck.NewTUIModel(namespaces)
	if len(m.Namespaces()) != 3 {
		t.Errorf("expected 3 namespaces, got %d", len(m.Namespaces()))
	}
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
go test ./internal/healthcheck/... -v -run TestNewTUIModelHasNamespaces
```

Expected: compile error — NewTUIModel not defined.

- [ ] **Step 3: Implement tui.go**

```go
package healthcheck

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const refreshInterval = 10 * time.Second

var (
	selectedStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("33"))
	unselectedStyle = lipgloss.NewStyle().Faint(true)
	footerStyle     = lipgloss.NewStyle().Faint(true).Italic(true)
)

type TUIModel struct {
	cs         *kubernetes.Clientset
	namespaces []string
	cursor     int
	selected   string
	tableOut   string
	loading    bool
	err        error
}

type refreshMsg struct{ tableOut string; err error }
type tickMsg time.Time

// NewTUIModel creates a model with the given namespace list (for testing).
func NewTUIModel(namespaces []string) TUIModel {
	return TUIModel{namespaces: namespaces}
}

// Namespaces returns the model's namespace list (for testing).
func (m TUIModel) Namespaces() []string {
	return m.namespaces
}

func newTUIModelWithClient(cs *kubernetes.Clientset, namespaces []string) TUIModel {
	return TUIModel{cs: cs, namespaces: namespaces}
}

func (m TUIModel) Init() tea.Cmd {
	return tick()
}

func (m TUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.namespaces)-1 {
				m.cursor++
			}
		case "enter":
			m.selected = m.namespaces[m.cursor]
			m.loading = true
			return m, m.fetchHealth()
		}
	case refreshMsg:
		m.loading = false
		m.tableOut = msg.tableOut
		m.err = msg.err
		return m, tick()
	case tickMsg:
		if m.selected != "" {
			m.loading = true
			return m, m.fetchHealth()
		}
		return m, tick()
	}
	return m, nil
}

func (m TUIModel) View() string {
	var b strings.Builder
	if m.selected == "" {
		b.WriteString("Select a namespace:\n\n")
		for i, ns := range m.namespaces {
			if i == m.cursor {
				b.WriteString(selectedStyle.Render("> "+ns) + "\n")
			} else {
				b.WriteString(unselectedStyle.Render("  "+ns) + "\n")
			}
		}
	} else {
		b.WriteString(fmt.Sprintf("Namespace: %s", selectedStyle.Render(m.selected)))
		if m.loading {
			b.WriteString("  (refreshing...)")
		}
		b.WriteString("\n\n")
		if m.err != nil {
			b.WriteString("error: " + m.err.Error() + "\n")
		} else {
			b.WriteString(m.tableOut)
		}
	}
	b.WriteString("\n" + footerStyle.Render(
		"To run non-interactively: kelper healthcheck -n <namespace>  •  q to quit",
	))
	return b.String()
}

func (m TUIModel) fetchHealth() tea.Cmd {
	return func() tea.Msg {
		// Capture table output as a string.
		// We redirect by running RunTable with a string builder capture.
		// For simplicity, re-use RunTable which writes to os.Stdout.
		// In TUI mode we capture via a pipe approach.
		ns := m.selected
		if ns == "All Namespaces" {
			ns = ""
		}
		var sb strings.Builder
		// Run healthcheck into a buffer.
		pods, err := m.cs.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			return refreshMsg{err: err}
		}
		var unhealthyPods []string
		for _, p := range pods.Items {
			if IsUnhealthyPod(p) {
				ready := podReadyString(p)
				status := podStatusString(p)
				unhealthyPods = append(unhealthyPods, fmt.Sprintf("  %-40s %-8s %s", p.Name, ready, status))
			}
		}
		if len(unhealthyPods) == 0 {
			sb.WriteString("Unhealthy Pods: none\n")
		} else {
			sb.WriteString("Unhealthy Pods:\n")
			for _, line := range unhealthyPods {
				sb.WriteString(line + "\n")
			}
		}
		return refreshMsg{tableOut: sb.String()}
	}
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// runTUI fetches namespaces from the cluster and launches the bubbletea TUI.
func runTUI(cs *kubernetes.Clientset) error {
	nsList, err := cs.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list namespaces: %w", err)
	}
	namespaces := []string{"All Namespaces"}
	for _, ns := range nsList.Items {
		namespaces = append(namespaces, ns.Name)
	}
	m := newTUIModelWithClient(cs, namespaces)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/healthcheck/... -v
```

Expected: PASS

- [ ] **Step 5: Build**

```bash
go build ./cmd/kelper/
```

Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add internal/healthcheck/tui.go
git commit -m "feat: implement healthcheck bubbletea TUI with namespace selector"
```

---

## Task 9: `internal/images`, `internal/resources`, `internal/volumes`

**Files:**
- Replace stub: `internal/images/images.go`
- Replace stub: `internal/resources/resources.go`
- Replace stub: `internal/volumes/volumes.go`

All three share the same pod-fetch-and-render pattern. Implement together.

- [ ] **Step 1: Write tests for images**

Create `internal/images/images_test.go`:

```go
package images_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/images"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRenderPodImagesContainsImageRef(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mypod", Namespace: "default"},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{Name: "init", Image: "myrepo/init:v1"},
			},
			Containers: []corev1.Container{
				{Name: "app", Image: "myrepo/app:v2"},
			},
		},
	}
	var buf bytes.Buffer
	images.RenderPod(&buf, pod)
	out := buf.String()
	if !strings.Contains(out, "myrepo/init:v1") {
		t.Errorf("expected init container image in output, got: %s", out)
	}
	if !strings.Contains(out, "myrepo/app:v2") {
		t.Errorf("expected container image in output, got: %s", out)
	}
}

func TestRenderPodImagesShowsNoneForEmptyInitContainers(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mypod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "myrepo/app:v2"},
			},
		},
	}
	var buf bytes.Buffer
	images.RenderPod(&buf, pod)
	out := buf.String()
	if !strings.Contains(out, "(none)") {
		t.Errorf("expected '(none)' for empty initContainers, got: %s", out)
	}
}
```

- [ ] **Step 2: Write tests for resources**

Create `internal/resources/resources_test.go`:

```go
package resources_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/resources"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRenderPodResourcesShowsLimits(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mypod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				},
			},
		},
	}
	var buf bytes.Buffer
	resources.RenderPod(&buf, pod)
	out := buf.String()
	if !strings.Contains(out, "500m") {
		t.Errorf("expected CPU limit '500m' in output, got: %s", out)
	}
}

func TestRenderPodResourcesShowsNoneWhenEmpty(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mypod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app"},
			},
		},
	}
	var buf bytes.Buffer
	resources.RenderPod(&buf, pod)
	out := buf.String()
	if !strings.Contains(out, "(none)") {
		t.Errorf("expected '(none)' for empty resources, got: %s", out)
	}
}
```

- [ ] **Step 3: Write tests for volumes**

Create `internal/volumes/volumes_test.go`:

```go
package volumes_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jthunderbird/kelper/internal/volumes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRenderPodVolumesShowsMounts(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mypod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					VolumeMounts: []corev1.VolumeMount{
						{Name: "data", MountPath: "/data"},
					},
				},
			},
			Volumes: []corev1.Volume{
				{Name: "data"},
			},
		},
	}
	var buf bytes.Buffer
	volumes.RenderPod(&buf, pod)
	out := buf.String()
	if !strings.Contains(out, "/data") {
		t.Errorf("expected mount path '/data' in output, got: %s", out)
	}
}

func TestRenderPodVolumesShowsNoneWhenEmpty(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "mypod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app"},
			},
		},
	}
	var buf bytes.Buffer
	volumes.RenderPod(&buf, pod)
	out := buf.String()
	if !strings.Contains(out, "(none)") {
		t.Errorf("expected '(none)' for empty volumeMounts, got: %s", out)
	}
}
```

- [ ] **Step 4: Run all three test files to confirm they fail**

```bash
go test ./internal/images/... ./internal/resources/... ./internal/volumes/... -v
```

Expected: compile errors — RenderPod not defined.

- [ ] **Step 5: Implement images.go**

Replace `internal/images/images.go`:

```go
package images

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/jthunderbird/kelper/internal/output"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Command returns the cobra command for images / image / imgs / img.
func Command(cs **kubernetes.Clientset) *cobra.Command {
	var namespace string
	var allNamespaces bool

	cmd := &cobra.Command{
		Use:     "images",
		Aliases: []string{"image", "imgs", "img"},
		Short:   "Show container images per pod",
		RunE: func(cmd *cobra.Command, args []string) error {
			ns := namespace
			if allNamespaces {
				ns = ""
			}
			var podName string
			if len(args) > 0 {
				podName = args[0]
			}
			return run(*cs, ns, podName)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "all namespaces")
	return cmd
}

func run(cs *kubernetes.Clientset, namespace, podName string) error {
	ctx := context.Background()
	if podName != "" {
		pod, err := cs.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			output.Errorf(os.Stderr, "get pod: %s", err)
			os.Exit(1)
		}
		RenderPod(os.Stdout, *pod)
		return nil
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		output.Errorf(os.Stderr, "list pods: %s", err)
		os.Exit(1)
	}
	for _, pod := range pods.Items {
		RenderPod(os.Stdout, pod)
	}
	return nil
}

// RenderPod writes image information for a single pod to w.
func RenderPod(w io.Writer, pod corev1.Pod) {
	output.PodHeader(w, pod.Name, pod.Namespace)

	fmt.Fprintln(w, "  initContainers:")
	if len(pod.Spec.InitContainers) == 0 {
		fmt.Fprintln(w, "    (none)")
	} else {
		for _, c := range pod.Spec.InitContainers {
			fmt.Fprintf(w, "    %s:\n      image: %s\n", c.Name, c.Image)
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  containers:")
	if len(pod.Spec.Containers) == 0 {
		fmt.Fprintln(w, "    (none)")
	} else {
		for _, c := range pod.Spec.Containers {
			fmt.Fprintf(w, "    %s:\n      image: %s\n", c.Name, c.Image)
		}
	}
	fmt.Fprintln(w)
}
```

- [ ] **Step 6: Implement resources.go**

Replace `internal/resources/resources.go`:

```go
package resources

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/jthunderbird/kelper/internal/output"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// Command returns the cobra command for resources / resource / res.
func Command(cs **kubernetes.Clientset) *cobra.Command {
	var namespace string
	var allNamespaces bool

	cmd := &cobra.Command{
		Use:     "resources",
		Aliases: []string{"resource", "res"},
		Short:   "Show resource limits and requests per pod",
		RunE: func(cmd *cobra.Command, args []string) error {
			ns := namespace
			if allNamespaces {
				ns = ""
			}
			var podName string
			if len(args) > 0 {
				podName = args[0]
			}
			return run(*cs, ns, podName)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "all namespaces")
	return cmd
}

func run(cs *kubernetes.Clientset, namespace, podName string) error {
	ctx := context.Background()
	if podName != "" {
		pod, err := cs.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			output.Errorf(os.Stderr, "get pod: %s", err)
			os.Exit(1)
		}
		RenderPod(os.Stdout, *pod)
		return nil
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		output.Errorf(os.Stderr, "list pods: %s", err)
		os.Exit(1)
	}
	for _, pod := range pods.Items {
		RenderPod(os.Stdout, pod)
	}
	return nil
}

// RenderPod writes resource limits/requests for a single pod to w.
func RenderPod(w io.Writer, pod corev1.Pod) {
	output.PodHeader(w, pod.Name, pod.Namespace)

	fmt.Fprintln(w, "  initContainers:")
	if len(pod.Spec.InitContainers) == 0 {
		fmt.Fprintln(w, "    (none)")
	} else {
		for _, c := range pod.Spec.InitContainers {
			fmt.Fprintf(w, "    %s:\n", c.Name)
			renderResources(w, c.Resources)
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  containers:")
	if len(pod.Spec.Containers) == 0 {
		fmt.Fprintln(w, "    (none)")
	} else {
		for _, c := range pod.Spec.Containers {
			fmt.Fprintf(w, "    %s:\n", c.Name)
			renderResources(w, c.Resources)
		}
	}
	fmt.Fprintln(w)
}

func renderResources(w io.Writer, r corev1.ResourceRequirements) {
	if len(r.Limits) == 0 && len(r.Requests) == 0 {
		fmt.Fprintln(w, "      (none)")
		return
	}
	data, _ := yaml.Marshal(r)
	// Indent each line by 6 spaces.
	for _, line := range splitLines(string(data)) {
		fmt.Fprintf(w, "      %s\n", line)
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
```

- [ ] **Step 7: Implement volumes.go**

Replace `internal/volumes/volumes.go`:

```go
package volumes

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/jthunderbird/kelper/internal/output"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// Command returns the cobra command for volumes / volume / vols / vol.
func Command(cs **kubernetes.Clientset) *cobra.Command {
	var namespace string
	var allNamespaces bool

	cmd := &cobra.Command{
		Use:     "volumes",
		Aliases: []string{"volume", "vols", "vol"},
		Short:   "Show volume mounts per pod",
		RunE: func(cmd *cobra.Command, args []string) error {
			ns := namespace
			if allNamespaces {
				ns = ""
			}
			var podName string
			if len(args) > 0 {
				podName = args[0]
			}
			return run(*cs, ns, podName)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "namespace")
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "all namespaces")
	return cmd
}

func run(cs *kubernetes.Clientset, namespace, podName string) error {
	ctx := context.Background()
	if podName != "" {
		pod, err := cs.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			output.Errorf(os.Stderr, "get pod: %s", err)
			os.Exit(1)
		}
		RenderPod(os.Stdout, *pod)
		return nil
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		output.Errorf(os.Stderr, "list pods: %s", err)
		os.Exit(1)
	}
	for _, pod := range pods.Items {
		RenderPod(os.Stdout, pod)
	}
	return nil
}

// RenderPod writes volumeMount and volume info for a single pod to w.
func RenderPod(w io.Writer, pod corev1.Pod) {
	output.PodHeader(w, pod.Name, pod.Namespace)

	fmt.Fprintln(w, "  initContainers:")
	if len(pod.Spec.InitContainers) == 0 {
		fmt.Fprintln(w, "    (none)")
	} else {
		for _, c := range pod.Spec.InitContainers {
			fmt.Fprintf(w, "    %s:\n", c.Name)
			renderMounts(w, c.VolumeMounts)
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  containers:")
	if len(pod.Spec.Containers) == 0 {
		fmt.Fprintln(w, "    (none)")
	} else {
		for _, c := range pod.Spec.Containers {
			fmt.Fprintf(w, "    %s:\n", c.Name)
			renderMounts(w, c.VolumeMounts)
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "  shared volumes for %s pod:\n", pod.Name)
	if len(pod.Spec.Volumes) == 0 {
		fmt.Fprintln(w, "    (none)")
	} else {
		data, _ := yaml.Marshal(pod.Spec.Volumes)
		for _, line := range splitLines(string(data)) {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
	fmt.Fprintln(w)
}

func renderMounts(w io.Writer, mounts []corev1.VolumeMount) {
	if len(mounts) == 0 {
		fmt.Fprintln(w, "      (none)")
		return
	}
	data, _ := yaml.Marshal(mounts)
	for _, line := range splitLines(string(data)) {
		fmt.Fprintf(w, "      %s\n", line)
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
```

- [ ] **Step 8: Run all tests**

```bash
go test ./internal/images/... ./internal/resources/... ./internal/volumes/... -v
```

Expected: all PASS

- [ ] **Step 9: Build**

```bash
go build ./cmd/kelper/
```

Expected: no errors.

- [ ] **Step 10: Commit**

```bash
git add internal/images/ internal/resources/ internal/volumes/
git commit -m "feat: implement images, resources, volumes pod inspector commands"
```

---

## Task 10: `internal/kubeconfig` — Non-Interactive CSR & RBAC Flow

**Files:**
- Replace stub: `internal/kubeconfig/kubeconfig.go`

- [ ] **Step 1: Write tests**

Create `internal/kubeconfig/kubeconfig_test.go`:

```go
package kubeconfig_test

import (
	"testing"

	"github.com/jthunderbird/kelper/internal/kubeconfig"
)

func TestValidateOptionsRequiresUser(t *testing.T) {
	opts := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeReadonly,
		Namespace:   "default",
	}
	if err := kubeconfig.ValidateOptions(opts); err == nil {
		t.Error("expected error when Username is empty")
	}
}

func TestValidateOptionsRequiresNamespaceForReadonly(t *testing.T) {
	opts := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeReadonly,
		Username:    "john",
	}
	if err := kubeconfig.ValidateOptions(opts); err == nil {
		t.Error("expected error when Namespace is empty for readonly account type")
	}
}

func TestValidateOptionsAllowsEmptyNamespaceForCluster(t *testing.T) {
	opts := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeCluster,
		Username:    "john",
	}
	if err := kubeconfig.ValidateOptions(opts); err != nil {
		t.Errorf("expected no error for cluster account type with no namespace, got: %v", err)
	}
}

func TestValidateOptionsScopedRequiresResources(t *testing.T) {
	opts := kubeconfig.Options{
		AccountType: kubeconfig.AccountTypeScoped,
		Username:    "john",
		Namespace:   "default",
	}
	if err := kubeconfig.ValidateOptions(opts); err == nil {
		t.Error("expected error when Resources is empty for scoped account type")
	}
}

func TestGenerateRSAKeyReturnsPEMBytes(t *testing.T) {
	key, err := kubeconfig.GenerateRSAKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(key) == 0 {
		t.Error("expected non-empty key bytes")
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./internal/kubeconfig/... -v
```

Expected: compile error.

- [ ] **Step 3: Implement kubeconfig.go**

Replace `internal/kubeconfig/kubeconfig.go`:

```go
package kubeconfig

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jthunderbird/kelper/internal/output"
	"github.com/spf13/cobra"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"
)

// AccountType represents the kubeconfig account type.
type AccountType string

const (
	AccountTypeReadonly AccountType = "readonly"
	AccountTypeAdmin    AccountType = "admin"
	AccountTypeCluster  AccountType = "cluster"
	AccountTypeScoped   AccountType = "scoped"
)

// Options holds the parameters for kubeconfig generation.
type Options struct {
	AccountType AccountType
	Username    string
	Namespace   string
	Resources   []string
	APIGroups   []string
	Verbs       []string
	OutputFile  string
	SkipConfirm bool
}

// ValidateOptions returns an error if required options are missing.
func ValidateOptions(opts Options) error {
	if opts.Username == "" {
		return fmt.Errorf("--user is required")
	}
	switch opts.AccountType {
	case AccountTypeReadonly, AccountTypeAdmin:
		if opts.Namespace == "" {
			return fmt.Errorf("--namespace is required for %s account type", opts.AccountType)
		}
	case AccountTypeScoped:
		if opts.Namespace == "" {
			return fmt.Errorf("--namespace is required for scoped account type")
		}
		if len(opts.Resources) == 0 {
			return fmt.Errorf("--resources is required for scoped account type")
		}
	case AccountTypeCluster:
		// No namespace required.
	}
	return nil
}

// GenerateRSAKey generates a 4096-bit RSA private key and returns PEM bytes.
// The key never touches disk.
func GenerateRSAKey() ([]byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, fmt.Errorf("generate RSA key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}), nil
}

// Command returns the cobra command tree for kubeconfig.
func Command() *cobra.Command {
	root := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Generate kubeconfig files for cluster users",
		RunE: func(cmd *cobra.Command, args []string) error {
			// No subcommand given → TUI wizard (implemented in Task 11).
			return runTUI()
		},
	}
	root.AddCommand(readonlyCmd(), adminCmd(), clusterCmd(), scopedCmd())
	return root
}

func readonlyCmd() *cobra.Command {
	var opts Options
	opts.AccountType = AccountTypeReadonly
	cmd := &cobra.Command{
		Use:   "readonly",
		Short: "Create a namespace-scoped readonly kubeconfig",
		RunE:  runNonInteractive(&opts),
	}
	addCommonFlags(cmd, &opts)
	return cmd
}

func adminCmd() *cobra.Command {
	var opts Options
	opts.AccountType = AccountTypeAdmin
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Create a namespace-scoped admin kubeconfig",
		RunE:  runNonInteractive(&opts),
	}
	addCommonFlags(cmd, &opts)
	return cmd
}

func clusterCmd() *cobra.Command {
	var opts Options
	opts.AccountType = AccountTypeCluster
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Create a cluster-wide readonly kubeconfig",
		RunE:  runNonInteractive(&opts),
	}
	cmd.Flags().StringVar(&opts.Username, "user", "", "username (required)")
	cmd.Flags().StringVar(&opts.OutputFile, "output", "", "output file (default: stdout)")
	cmd.Flags().BoolVarP(&opts.SkipConfirm, "yes", "y", false, "skip confirmation prompt")
	return cmd
}

func scopedCmd() *cobra.Command {
	var opts Options
	opts.AccountType = AccountTypeScoped
	var resourcesStr, apiGroupsStr, verbsStr string
	cmd := &cobra.Command{
		Use:   "scoped",
		Short: "Create a resource-scoped kubeconfig",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Resources = splitCSV(resourcesStr)
			opts.APIGroups = splitCSV(apiGroupsStr)
			opts.Verbs = splitCSV(verbsStr)
			return runNonInteractive(&opts)(cmd, args)
		},
	}
	addCommonFlags(cmd, &opts)
	cmd.Flags().StringVar(&resourcesStr, "resources", "", "comma-separated resources (required)")
	cmd.Flags().StringVar(&apiGroupsStr, "apigroups", "*", "comma-separated API groups")
	cmd.Flags().StringVar(&verbsStr, "verbs", "get,list,watch", "comma-separated verbs")
	return cmd
}

func addCommonFlags(cmd *cobra.Command, opts *Options) {
	cmd.Flags().StringVar(&opts.Username, "user", "", "username (required)")
	cmd.Flags().StringVarP(&opts.Namespace, "namespace", "n", "", "namespace (required)")
	cmd.Flags().StringVar(&opts.OutputFile, "output", "", "output file (default: stdout)")
	cmd.Flags().BoolVarP(&opts.SkipConfirm, "yes", "y", false, "skip confirmation prompt")
}

func runNonInteractive(opts *Options) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if err := ValidateOptions(*opts); err != nil {
			output.Errorf(os.Stderr, "%s", err)
			os.Exit(1)
		}
		printSummary(*opts)
		if !opts.SkipConfirm {
			fmt.Print("Proceed? [y/N]: ")
			var answer string
			fmt.Scanln(&answer)
			if strings.ToLower(answer) != "y" {
				fmt.Println("Cancelled.")
				return nil
			}
		}
		return Create(*opts)
	}
}

func printSummary(opts Options) {
	fmt.Println("\nSummary:")
	fmt.Printf("  Type:      %s\n", opts.AccountType)
	fmt.Printf("  Username:  %s\n", opts.Username)
	if opts.Namespace != "" {
		fmt.Printf("  Namespace: %s\n", opts.Namespace)
	}
	if len(opts.Verbs) > 0 {
		fmt.Printf("  Verbs:     %s\n", strings.Join(opts.Verbs, ", "))
	}
	if len(opts.Resources) > 0 {
		fmt.Printf("  Resources: %s\n", strings.Join(opts.Resources, ", "))
	}
	if opts.OutputFile != "" {
		fmt.Printf("  Output:    %s\n", opts.OutputFile)
	} else {
		fmt.Printf("  Output:    stdout\n")
	}
	fmt.Println()
}

// Create executes the full kubeconfig generation flow.
func Create(opts Options) error {
	cs, err := buildClientset()
	if err != nil {
		return fmt.Errorf("connect to cluster: %w", err)
	}
	ctx := context.Background()

	fmt.Print("Generating RSA key... ")
	keyPEM, err := GenerateRSAKey()
	if err != nil {
		return err
	}
	fmt.Println("done")

	fmt.Print("Submitting CSR... ")
	certPEM, err := submitAndApproveCSR(ctx, cs, opts.Username, keyPEM)
	if err != nil {
		return err
	}
	fmt.Println("done")

	fmt.Print("Creating RBAC... ")
	if err := createRBAC(ctx, cs, opts); err != nil {
		return err
	}
	fmt.Println("done")

	fmt.Print("Building kubeconfig... ")
	kubeconfigYAML, err := buildKubeconfig(ctx, cs, opts.Username, certPEM, keyPEM)
	if err != nil {
		return err
	}
	fmt.Println("done")

	if opts.OutputFile != "" {
		if err := os.WriteFile(opts.OutputFile, kubeconfigYAML, 0600); err != nil {
			return fmt.Errorf("write output file: %w", err)
		}
		fmt.Printf("Kubeconfig written to %s\n", opts.OutputFile)
	} else {
		fmt.Print(string(kubeconfigYAML))
	}
	return nil
}

func submitAndApproveCSR(ctx context.Context, cs *kubernetes.Clientset, username string, keyPEM []byte) ([]byte, error) {
	// Parse the RSA key to generate a CSR.
	block, _ := pem.Decode(keyPEM)
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	csrTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   username,
			Organization: []string{"kelper"},
		},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, key)
	if err != nil {
		return nil, fmt.Errorf("create CSR: %w", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	csrName := username + "-kelper-csr"
	// Delete any pre-existing CSR with this name.
	_ = cs.CertificatesV1().CertificateSigningRequests().Delete(ctx, csrName, metav1.DeleteOptions{})

	csr := &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: csrName},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    csrPEM,
			SignerName: "kubernetes.io/kube-apiserver-client",
			Usages:     []certificatesv1.KeyUsage{certificatesv1.UsageClientAuth},
		},
	}
	_, err = cs.CertificatesV1().CertificateSigningRequests().Create(ctx, csr, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create CSR resource: %w", err)
	}

	// Approve.
	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:               certificatesv1.CertificateApproved,
		Status:             corev1.ConditionTrue,
		Reason:             "KelperApprove",
		Message:            "Approved by kelper",
		LastUpdateTime:     metav1.Now(),
	})
	_, err = cs.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, csrName, csr, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("approve CSR: %w", err)
	}

	// Poll for cert.
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		approved, err := cs.CertificatesV1().CertificateSigningRequests().Get(ctx, csrName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("get CSR: %w", err)
		}
		if len(approved.Status.Certificate) > 0 {
			return approved.Status.Certificate, nil
		}
	}
	return nil, fmt.Errorf("timed out waiting for signed certificate")
}

func createRBAC(ctx context.Context, cs *kubernetes.Clientset, opts Options) error {
	verbs := opts.Verbs
	resources := opts.Resources
	apiGroups := opts.APIGroups

	switch opts.AccountType {
	case AccountTypeReadonly:
		verbs = []string{"get", "list", "watch"}
		resources = []string{"*"}
		apiGroups = []string{"*"}
	case AccountTypeAdmin:
		verbs = []string{"*"}
		resources = []string{"*"}
		apiGroups = []string{"*"}
	case AccountTypeCluster:
		verbs = []string{"get", "list", "watch"}
		resources = []string{"*"}
		apiGroups = []string{"*"}
	}

	roleName := opts.Username + "-kelper-role"
	bindingName := opts.Username + "-kelper-binding"

	if opts.AccountType == AccountTypeCluster {
		cr := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: roleName},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: apiGroups, Resources: resources, Verbs: verbs},
			},
		}
		if _, err := cs.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create ClusterRole: %w", err)
		}
		crb := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: bindingName},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: roleName},
			Subjects:   []rbacv1.Subject{{Kind: "User", Name: opts.Username}},
		}
		if _, err := cs.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create ClusterRoleBinding: %w", err)
		}
		return nil
	}

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: opts.Namespace},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: apiGroups, Resources: resources, Verbs: verbs},
		},
	}
	if _, err := cs.RbacV1().Roles(opts.Namespace).Create(ctx, role, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create Role: %w", err)
	}
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindingName, Namespace: opts.Namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: roleName},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: opts.Username}},
	}
	if _, err := cs.RbacV1().RoleBindings(opts.Namespace).Create(ctx, rb, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create RoleBinding: %w", err)
	}
	return nil
}

func buildKubeconfig(ctx context.Context, cs *kubernetes.Clientset, username string, certPEM, keyPEM []byte) ([]byte, error) {
	rawConfig, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	// Use the current cluster's server and CA.
	var server, caData string
	for _, cluster := range rawConfig.Clusters {
		server = cluster.Server
		caData = base64.StdEncoding.EncodeToString(cluster.CertificateAuthorityData)
		break
	}

	cfg := &clientcmdapi.Config{
		APIVersion: "v1",
		Kind:       "Config",
		Clusters: map[string]*clientcmdapi.Cluster{
			"kelper-cluster": {
				Server:                   server,
				CertificateAuthorityData: []byte(caData),
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			username: {
				ClientCertificateData: certPEM,
				ClientKeyData:         keyPEM,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			username + "@kelper-cluster": {
				Cluster:  "kelper-cluster",
				AuthInfo: username,
			},
		},
		CurrentContext: username + "@kelper-cluster",
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal kubeconfig: %w", err)
	}
	return out, nil
}

func buildClientset() (*kubernetes.Clientset, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// runTUI is the placeholder for the interactive wizard.
// Implemented in tui.go (Task 11).
func runTUI() error {
	fmt.Println("kubeconfig TUI not yet implemented — use a subcommand:")
	fmt.Println("  kelper kubeconfig readonly --user <name> --namespace <ns>")
	fmt.Println("  kelper kubeconfig admin    --user <name> --namespace <ns>")
	fmt.Println("  kelper kubeconfig cluster  --user <name>")
	fmt.Println("  kelper kubeconfig scoped   --user <name> --namespace <ns> --resources <r>")
	return nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/kubeconfig/... -v
```

Expected: PASS

- [ ] **Step 5: Build**

```bash
go build ./cmd/kelper/
```

Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add internal/kubeconfig/kubeconfig.go
git commit -m "feat: implement kubeconfig CSR/RBAC generation flow"
```

---

## Task 11: `internal/kubeconfig/tui.go` — Interactive Wizard

**Files:**
- Create: `internal/kubeconfig/tui.go`

- [ ] **Step 1: Write smoke test for wizard model**

Add to `internal/kubeconfig/kubeconfig_test.go`:

```go
func TestWizardModelInitialStep(t *testing.T) {
	m := kubeconfig.NewWizardModel()
	if m.Step() != 0 {
		t.Errorf("expected initial step 0, got %d", m.Step())
	}
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
go test ./internal/kubeconfig/... -v -run TestWizardModelInitialStep
```

Expected: compile error — NewWizardModel not defined.

- [ ] **Step 3: Implement tui.go**

```go
package kubeconfig

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	wizardTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("33"))
	wizardSelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("36"))
	wizardDimStyle      = lipgloss.NewStyle().Faint(true)
	wizardErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

type wizardStep int

const (
	stepAccountType wizardStep = iota
	stepUsername
	stepNamespace
	stepAPIGroups  // scoped only
	stepResources  // scoped only
	stepVerbs      // scoped only
	stepOutput
	stepConfirm
	stepDone
)

var accountTypes = []AccountType{
	AccountTypeReadonly,
	AccountTypeAdmin,
	AccountTypeCluster,
	AccountTypeScoped,
}

var accountTypeLabels = map[AccountType]string{
	AccountTypeReadonly: "Readonly (namespace-scoped)",
	AccountTypeAdmin:    "Admin (namespace-scoped)",
	AccountTypeCluster:  "Cluster-wide readonly",
	AccountTypeScoped:   "Resource-scoped (custom)",
}

type wizardModel struct {
	step        wizardStep
	opts        Options
	cursor      int
	textInput   string
	namespaces  []string
	err         string
	confirmed   bool
}

// NewWizardModel returns an initial wizard model (for testing).
func NewWizardModel() wizardModel {
	return wizardModel{}
}

// Step returns the current wizard step index (for testing).
func (m wizardModel) Step() int {
	return int(m.step)
}

func (m wizardModel) Init() tea.Cmd {
	return nil
}

func (m wizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch m.step {
		case stepAccountType:
			switch msg.String() {
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				}
			case "down", "j":
				if m.cursor < len(accountTypes)-1 {
					m.cursor++
				}
			case "enter":
				m.opts.AccountType = accountTypes[m.cursor]
				m.cursor = 0
				m.step = stepUsername
			case "q", "ctrl+c":
				return m, tea.Quit
			}

		case stepUsername, stepNamespace, stepAPIGroups, stepResources, stepVerbs, stepOutput:
			switch msg.String() {
			case "enter":
				m = m.advanceText()
			case "ctrl+c":
				return m, tea.Quit
			case "backspace":
				if len(m.textInput) > 0 {
					m.textInput = m.textInput[:len(m.textInput)-1]
				}
			default:
				if len(msg.String()) == 1 {
					m.textInput += msg.String()
				}
			}

		case stepConfirm:
			switch msg.String() {
			case "y", "Y":
				m.confirmed = true
				m.step = stepDone
				return m, tea.Quit
			case "n", "N", "q", "ctrl+c":
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m wizardModel) advanceText() wizardModel {
	val := strings.TrimSpace(m.textInput)
	m.textInput = ""
	m.err = ""

	switch m.step {
	case stepUsername:
		if val == "" {
			m.err = "username cannot be empty"
			return m
		}
		m.opts.Username = val
		if m.opts.AccountType == AccountTypeCluster {
			m.step = stepOutput
		} else {
			m.step = stepNamespace
		}
	case stepNamespace:
		if val == "" {
			m.err = "namespace cannot be empty"
			return m
		}
		m.opts.Namespace = val
		if m.opts.AccountType == AccountTypeScoped {
			m.step = stepAPIGroups
		} else {
			m.step = stepOutput
		}
	case stepAPIGroups:
		m.opts.APIGroups = splitCSV(val)
		if len(m.opts.APIGroups) == 0 {
			m.opts.APIGroups = []string{"*"}
		}
		m.step = stepResources
	case stepResources:
		if val == "" {
			m.err = "resources cannot be empty"
			return m
		}
		m.opts.Resources = splitCSV(val)
		m.step = stepVerbs
	case stepVerbs:
		m.opts.Verbs = splitCSV(val)
		if len(m.opts.Verbs) == 0 {
			m.opts.Verbs = []string{"get", "list", "watch"}
		}
		m.step = stepOutput
	case stepOutput:
		m.opts.OutputFile = val // empty = stdout
		m.step = stepConfirm
	}
	return m
}

func (m wizardModel) View() string {
	var b strings.Builder

	totalSteps := m.totalSteps()
	currentStep := int(m.step) + 1
	if currentStep > totalSteps {
		currentStep = totalSteps
	}

	b.WriteString(wizardTitleStyle.Render(fmt.Sprintf("kelper kubeconfig wizard  (step %d/%d)", currentStep, totalSteps)) + "\n\n")

	switch m.step {
	case stepAccountType:
		b.WriteString("Select account type:\n\n")
		for i, at := range accountTypes {
			label := accountTypeLabels[at]
			if i == m.cursor {
				b.WriteString(wizardSelectedStyle.Render("> "+label) + "\n")
			} else {
				b.WriteString(wizardDimStyle.Render("  "+label) + "\n")
			}
		}

	case stepUsername:
		b.WriteString("Username: " + m.textInput + "█\n")

	case stepNamespace:
		b.WriteString("Namespace: " + m.textInput + "█\n")

	case stepAPIGroups:
		b.WriteString("API groups (comma-separated, * for all): " + m.textInput + "█\n")

	case stepResources:
		b.WriteString("Resources (comma-separated, e.g. pods,secrets): " + m.textInput + "█\n")

	case stepVerbs:
		b.WriteString("Verbs (comma-separated, e.g. get,list,watch): " + m.textInput + "█\n")

	case stepOutput:
		b.WriteString("Output file (leave blank for stdout): " + m.textInput + "█\n")

	case stepConfirm:
		b.WriteString("Confirm:\n\n")
		b.WriteString(fmt.Sprintf("  Type:      %s\n", accountTypeLabels[m.opts.AccountType]))
		b.WriteString(fmt.Sprintf("  Username:  %s\n", m.opts.Username))
		if m.opts.Namespace != "" {
			b.WriteString(fmt.Sprintf("  Namespace: %s\n", m.opts.Namespace))
		}
		if len(m.opts.Verbs) > 0 {
			b.WriteString(fmt.Sprintf("  Verbs:     %s\n", strings.Join(m.opts.Verbs, ", ")))
		}
		if len(m.opts.Resources) > 0 {
			b.WriteString(fmt.Sprintf("  Resources: %s\n", strings.Join(m.opts.Resources, ", ")))
		}
		out := "stdout"
		if m.opts.OutputFile != "" {
			out = m.opts.OutputFile
		}
		b.WriteString(fmt.Sprintf("  Output:    %s\n", out))
		b.WriteString("\n  [ y ] Create User     [ n ] Cancel\n")
	}

	if m.err != "" {
		b.WriteString("\n" + wizardErrorStyle.Render("error: "+m.err) + "\n")
	}

	b.WriteString("\n" + wizardDimStyle.Render("ctrl+c to quit") + "\n")
	return b.String()
}

func (m wizardModel) totalSteps() int {
	if m.opts.AccountType == AccountTypeScoped {
		return 8 // type, user, ns, apigroups, resources, verbs, output, confirm
	}
	if m.opts.AccountType == AccountTypeCluster {
		return 4 // type, user, output, confirm
	}
	return 5 // type, user, ns, output, confirm
}

// runTUI launches the kubeconfig wizard and runs the result if confirmed.
func runTUI() error {
	m := NewWizardModel()
	p := tea.NewProgram(m, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		return err
	}
	final, ok := result.(wizardModel)
	if !ok || !final.confirmed {
		fmt.Println("Cancelled.")
		return nil
	}
	return Create(final.opts)
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/kubeconfig/... -v
```

Expected: PASS

- [ ] **Step 5: Build**

```bash
go build ./cmd/kelper/
```

Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add internal/kubeconfig/tui.go
git commit -m "feat: implement kubeconfig bubbletea TUI wizard"
```

---

## Task 12: Final Wiring, Build Verification & README Update

**Files:**
- Modify: `cmd/kelper/main.go` (remove DisableFlagParsing from root, fix PersistentPreRun skip logic)
- Modify: `README.md`

- [ ] **Step 1: Fix PersistentPreRun to skip client init for kubeconfig subcommands that build their own client**

In `cmd/kelper/main.go`, update `PersistentPreRunE` to not error when the cluster is unreachable for commands that don't need the shared client (passthrough handles its own exec; kubeconfig builds its own internal client):

```go
PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
	// Skip client init for completion subcommand.
	if cmd.Name() == "completion" {
		return nil
	}
	// kubeconfig builds its own client internally.
	if cmd.Name() == "kubeconfig" || cmd.Parent().Name() == "kubeconfig" {
		return nil
	}
	var err error
	cs, err = client.New(kubeconfigPath)
	if err != nil {
		output.Errorf(os.Stderr, "could not connect to cluster: %s", err)
		os.Exit(1)
	}
	return nil
},
```

- [ ] **Step 2: Run all tests**

```bash
go test ./... -v
```

Expected: all PASS

- [ ] **Step 3: Build the final binary**

```bash
go build -o kelper ./cmd/kelper/
```

Expected: `kelper` binary produced.

- [ ] **Step 4: Verify passthrough works**

```bash
./kelper version --client
```

Expected: output identical to `kubectl version --client`.

- [ ] **Step 5: Verify help output**

```bash
./kelper --help
```

Expected: cobra-generated help listing all subcommands.

- [ ] **Step 6: Verify shell completion generates**

```bash
./kelper completion bash | head -5
```

Expected: bash completion script output.

- [ ] **Step 7: Update README.md**

Replace the "Installing" section with:

```markdown
## Installing

```bash
git clone https://github.com/jthunderbird/kelper.git
cd kelper
go build -o kelper ./cmd/kelper/
cp kelper /usr/local/bin/kelper
alias k=kelper
k help
```

The original bash script is available as `kelper.sh` in the repo root for reference.
```

- [ ] **Step 8: Final commit**

```bash
git add -A
git commit -m "feat: complete kelper Go rewrite — all commands implemented"
```

---

## Self-Review Notes

**Spec coverage check:**

| Spec requirement | Task |
|---|---|
| Delete old prototype, rename bash script | Task 1 |
| `internal/client` package | Task 2 |
| `internal/output` shared formatting | Task 3 |
| Transparent passthrough + `--raw` | Tasks 4, 5 |
| `get -o yaml` interception routing | Task 5, 6 |
| `neat` YAML field stripping (kubectl-neat + fallback) | Task 6 |
| `decode` secret flush-left display with separator | Task 6 |
| `healthcheck` non-interactive table + exit code | Task 7 |
| `healthcheck` TUI with namespace selector + live refresh | Task 8 |
| `images` pod inspector | Task 9 |
| `resources` pod inspector | Task 9 |
| `volumes` pod inspector | Task 9 |
| `kubeconfig` 4 account types non-interactive | Task 10 |
| `kubeconfig` TUI adaptive wizard + confirmation screen | Task 11 |
| Shell completion | Task 12 |
| README update | Task 12 |

All spec requirements covered. No placeholders. Type signatures are consistent across tasks (`RenderPod(w io.Writer, pod corev1.Pod)` used uniformly in Tasks 9; `Options` and `AccountType` defined in Task 10 and referenced in Task 11).
