# Kelper Go Rewrite Design

**Date:** 2026-06-02  
**Status:** Approved  
**Scope:** Full rewrite of the `kelper` bash wrapper into a Go binary using cobra + client-go + bubbletea

---

## Background

Kelper is a kubectl wrapper that adds: YAML field stripping (`neat`), Secret decoding (`decode`), cluster healthcheck, pod image/resource/volume inspection, and kubeconfig generation. The current implementation is a single 424-line bash script with external dependencies on `yq`, `openssl`, `sed`, `awk`, and `grep`.

The Go rewrite eliminates all external tool dependencies, replaces subprocess-per-container kubectl calls with single client-go API calls, adds proper TUI interactivity, and fixes all known bash bugs.

---

## Repository Changes

- `cmd/kelp/main.go` deleted (old abandoned prototype)
- Compiled binaries `kelp` and `main` deleted
- Bash script renamed `kelper.sh` — stays in repo root as a lightweight alternative for users who want easy bash extensibility
- `go.mod` module path: `github.com/jthunderbird/kelper` (drop old `/m/v2` suffix)
- Go minimum version: `1.21`

---

## Project Structure

```
kelper/
├── cmd/
│   └── kelper/
│       └── main.go              # entrypoint, cobra root command
├── internal/
│   ├── client/
│   │   └── client.go            # client-go setup, kubeconfig loading
│   ├── get/
│   │   └── get.go               # intercepts get -o yaml, routes to neat/decode
│   ├── neat/
│   │   └── neat.go              # YAML field stripping logic
│   ├── decode/
│   │   └── decode.go            # Secret base64 decode + display
│   ├── healthcheck/
│   │   ├── healthcheck.go       # non-interactive table mode
│   │   └── tui.go               # bubbletea TUI for bare kelper healthcheck
│   ├── images/
│   │   └── images.go            # image inspector
│   ├── resources/
│   │   └── resources.go         # resource limits/requests inspector
│   ├── volumes/
│   │   └── volumes.go           # volumeMount inspector
│   ├── kubeconfig/
│   │   ├── kubeconfig.go        # non-interactive path + CSR/RBAC logic
│   │   └── tui.go               # bubbletea wizard for bare kelper kubeconfig
│   ├── passthrough/
│   │   └── passthrough.go       # shells out to kubectl for unrecognized commands
│   └── output/
│       └── output.go            # shared formatting: tables, YAML indent, colors
├── docs/
│   └── superpowers/
│       └── specs/
├── kelper.sh                    # original bash script (renamed)
├── go.mod
├── go.sum
├── LICENSE
└── README.md
```

---

## Dependencies

| Package | Purpose |
|---|---|
| `github.com/spf13/cobra` | CLI framework, subcommand dispatch, shell completion |
| `k8s.io/client-go` | All Kubernetes API calls |
| `k8s.io/api` + `k8s.io/apimachinery` | Kubernetes types |
| `sigs.k8s.io/yaml` | YAML marshal/unmarshal |
| `github.com/charmbracelet/bubbletea` | TUI runtime |
| `github.com/charmbracelet/lipgloss` | Terminal styling (colors, separators, headers) |
| `github.com/olekukonko/tablewriter` | Table formatting |
| `sigs.k8s.io/kubectl-neat` | Smart field stripping by resource type (fallback: structured denylist per kind) |

---

## CLI Command Surface

```
kelper <kubectl args>                        # transparent passthrough to kubectl
kelper get ... -o yaml                       # intercepted: neat or decode
kelper get ... -o yaml --raw                 # bypass interception, raw kubectl output

kelper healthcheck                           # TUI mode (interactive, no args)
kelper healthcheck -n <ns>                   # table mode + exit code
kelper healthcheck -A                        # table mode, all namespaces
kelper health [same flags]                   # alias

kelper images [pod] [-n ns] [-A]             # table header + indented YAML blocks
kelper image  [pod] [-n ns] [-A]             # alias
kelper imgs   [pod] [-n ns] [-A]             # alias
kelper img    [pod] [-n ns] [-A]             # alias

kelper resources [pod] [-n ns] [-A]          # table header + indented YAML blocks
kelper resource  [pod] [-n ns] [-A]          # alias
kelper res       [pod] [-n ns] [-A]          # alias

kelper volumes [pod] [-n ns] [-A]            # table header + indented YAML blocks
kelper volume  [pod] [-n ns] [-A]            # alias
kelper vols    [pod] [-n ns] [-A]            # alias
kelper vol     [pod] [-n ns] [-A]            # alias

kelper kubeconfig                              # TUI wizard (interactive, no args)
kelper kubeconfig readonly  [--user <name>] [--namespace <ns>] [--output <file>] [-y]
kelper kubeconfig admin     [--user <name>] [--namespace <ns>] [--output <file>] [-y]
kelper kubeconfig cluster   [--user <name>] [--output <file>] [-y]
kelper kubeconfig scoped    [--user <name>] [--namespace <ns>] [--resources <r>] [--apigroups <g>] [--verbs <v>] [--output <file>] [-y]

kelper completion bash|zsh|fish              # shell completion (cobra built-in)
```

**Aliases** are registered as cobra command aliases on the same handler — not separate commands.

**TUI trigger rule:** If a subcommand is invoked with no targeting arguments (no pod name, no `-n`, no `-A`), the TUI is launched. Any targeting argument present → non-interactive mode.

---

## Feature Designs

### `get` Interception & `--raw`

The cobra root command checks if the first non-flag argument is `get` AND `-o yaml` or `-oyaml` is present. If `--raw` is also present anywhere in args, interception is bypassed entirely and args are forwarded to passthrough, which execs `kubectl` streaming stdin/stdout/stderr — byte-for-byte identical to real kubectl. `--raw` inherits kubectl's exit code exactly.

### `neat` — YAML Field Stripping

Uses `sigs.k8s.io/kubectl-neat` to strip server-populated default fields based on resource type and OpenAPI schema. This handles CRDs and all standard resource kinds dynamically — no hardcoded denylist required. If the library is not cleanly importable, fallback is a structured denylist organized per resource kind (Deployment, Service, Pod, etc.) plus a generic set applied to all kinds.

Fixes bash limitation: `kind: List` is handled natively.

### `decode` — Secret Display

Detects `kind: Secret` by reading `.kind` from YAML. Decodes all `.data` values from base64 using `encoding/base64`. Handles `kind: List` of Secrets by iterating each item.

Output format — flush left, zero indentation, `KEY:` label highlighted, unicode separator line, blank line between entries:

```
KEY: username
─────────────────────────
admin

KEY: password
─────────────────────────
prom-operator

KEY: tls.crt
─────────────────────────
-----BEGIN CERTIFICATE-----
MIIBvzCCAWWgAwIBAgIRAIx...
-----END CERTIFICATE-----
```

### `healthcheck`

**Non-interactive mode** (any targeting flag present):

Fetches Pods + Deployments + StatefulSets + DaemonSets + Jobs + CronJobs in targeted scope via client-go. Renders two tables. Exits `1` if anything unhealthy, `0` if all healthy.

```
Unhealthy Pods
NAMESPACE       NAME                        READY   STATUS
istio-operator  istio-op-7765959ff8-fpsnz   0/1     CrashLoopBackOff

Unhealthy Workloads
NAMESPACE       KIND        NAME             AVAILABLE   DESIRED
istio-operator  job.batch   istiod-hook      0           1

Summary: 1 unhealthy pod, 1 unhealthy workload
```

**Interactive TUI mode** (bare `kelper healthcheck`):

bubbletea model with namespace selector list (fetched on startup). Once namespace selected (or "All Namespaces"), renders same table output with live refresh (default 10s). Footer: `"To run non-interactively: kelper healthcheck -n <namespace>"`. `q` or `ctrl+c` exits.

Fixes bash omissions: DaemonSets and CronJobs now included.

### `images`, `resources`, `volumes`

Single client-go Pod fetch per targeted scope. Pod header line colored/highlighted. YAML blocks rendered with 2-space indentation via `sigs.k8s.io/yaml`. Copy-paste safe.

Example (`images`):
```
pod: kyverno-admission-controller-5d8986c8b6-2g7gr (-n kyverno)
────────────────────────────────────────────────────────────────

  initContainers:
    kyverno-pre:
      image: registry1.dso.mil/ironbank/opensource/kyverno/kyvernopre:v1.13.4

  containers:
    kyverno:
      image: registry1.dso.mil/ironbank/opensource/kyverno:v1.13.4
```

Fixes:
- Single client-go call replaces N subprocess forks per container
- `kind: List` handled natively
- Empty containers/volumes print `(none)` explicitly rather than blank blocks

### `kubeconfig`

**Account types:**

| Type | Scope | Verbs | Resources |
|---|---|---|---|
| `readonly` | namespace-scoped | get, list, watch | all (`*`) |
| `admin` | namespace-scoped (or cluster-wide if no `--namespace`) | all | all (`*`) |
| `cluster-wide readonly` | cluster-wide | get, list, watch | all (`*`) |
| `scoped` | namespace-scoped | user-specified | user-specified resources + apiGroups |

**Non-interactive flow:**

1. Validate flags
2. Generate RSA key **in memory** (no temp files — fixes bash security bug)
3. Submit `CertificateSigningRequest` via client-go certs API
4. Approve CSR programmatically via client-go
5. Poll for signed cert with retry loop (replaces bash `sleep 1`)
6. Create Role/ClusterRole + RoleBinding/ClusterRoleBinding via client-go
7. Construct kubeconfig struct, marshal to YAML
8. Write to `--output` file or stdout if not specified
9. Confirmation summary printed before any cluster writes; `-y` flag skips confirmation

**Interactive TUI wizard** (bare `kelper kubeconfig`):

Adaptive step flow based on account type selection:

- Readonly / Admin: account type → username → namespace → output → confirmation
- Cluster-wide readonly: account type → username → output → confirmation
- Scoped: account type → username → namespace → API groups → resources → verbs → output → confirmation

Final confirmation screen (both modes):
```
Confirm

  Type:       Readonly (namespace-scoped)
  Username:   john_doe
  Namespace:  kyverno
  Verbs:      get, list, watch
  Resources:  all
  Output:     ./john-doe-readonly.yaml

  [ Create User ]   [ Cancel ]
```

Progress shown inline after confirmation: CSR submitted → approved → cert issued → RBAC created → kubeconfig written.

---

## Shared Output (`internal/output`)

- **Tables:** `github.com/olekukonko/tablewriter`
- **Colors/styling:** `github.com/charmbracelet/lipgloss`
- **YAML:** `sigs.k8s.io/yaml`
- All command output → `os.Stdout`
- All errors → `os.Stderr` with consistent `error: <message>` prefix

---

## Error Handling

| Scenario | Behavior |
|---|---|
| Cluster unreachable | Clear message to stderr, exit `1` |
| CSR approval timeout | Message + cleanup of partial RBAC, exit `1` |
| Unknown subcommand | Forwarded to kubectl passthrough; kubectl's own error handling and exit code |
| `--raw` / passthrough | kubectl exit code inherited exactly |
| kubeconfig wizard cancelled | Exit `0`, nothing written to cluster |

---

## Shell Completion

`kelper completion bash|zsh|fish` — generated automatically by cobra, no extra implementation required.

---

## Known Bash Bugs Fixed

| Bug | Fix |
|---|---|
| `neat`/`decode` no `kind: List` support | Handled natively in Go |
| `terminationGracePerionSeconds` typo in denylist | Irrelevant — kubectl-neat uses schema, not hardcoded names |
| `healthcheck` missing DaemonSets, CronJobs | Both included in client-go query |
| `healthcheck` no watch mode | bubbletea TUI with live refresh |
| kubeconfig temp key file not cleaned up | RSA key generated and held in memory only |
| kubeconfig `sleep 1` for cert propagation | Replaced with retry poll loop |
| kubeconfig no namespace-scoped path | All four account types implemented |
| N subprocess forks per container in images/resources/volumes | Single client-go Pod fetch |
