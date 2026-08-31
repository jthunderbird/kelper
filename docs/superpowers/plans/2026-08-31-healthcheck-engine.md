# Kelper Healthcheck Engine & Health Dashboard Plan

> **For agentic workers:** Steps use checkbox (`- [ ]`) syntax for tracking. Tasks are ordered by dependency; Task 1 and 2 gate everything else.

**Goal:** Replace kelper's two hardcoded health passes with a pluggable diagnosis engine producing severity-ranked `Finding`s, add a k9s-style health monitoring dashboard, add continuous watch with history, and embed k9s as `kelper k9s`.

**Architecture:** A `Snapshot` fetches all cluster state once, concurrently. Independent `Check` implementations are pure functions over that snapshot, emitting `Finding`s. Renderers (CLI table, tview dashboard, watch differ) consume `Finding`s and know nothing about Kubernetes types. Capability detection disables checks whose data sources are unavailable and reports them in one status line.

**Tech Stack:** Go 1.25, k8s.io/* v0.35.3, cobra, tview/tcell (dashboard), lipgloss, tablewriter, client-go dynamic + discovery, metrics.k8s.io, embedded k9s v0.51.0.

**Decisions (confirmed with maintainer):**

| Decision | Choice |
|---|---|
| k9s integration | Import `github.com/derailed/k9s/cmd`, embed in binary |
| Dashboard TUI | tview/tcell (arrives free with the k9s dependency) |
| Missing metrics sources | Degrade silently, report one status line |
| Output compatibility | Free to break; full refactor |
| Delivery | Single effort, not phased |

---

## Validated Spikes

These were built and run before planning; do not re-litigate them.

| Assumption | Result |
|---|---|
| kelper compiles on k8s.io v0.35.3 + go 1.25 | Yes — `go build`, `go vet`, and all tests pass after `go mod tidy` |
| `github.com/derailed/k9s/cmd` is importable | Yes — `cmd.Execute()` is public and works |
| Combined kelper + k9s builds | Yes — no dependency conflicts |
| `kelper k9s` launches k9s | Yes, after two fixes (below) |
| Binary size cost | **54MB → 235MB** unstripped; 168MB with `-ldflags "-s -w"` |

**k9s integration gotchas found by spike:**

1. `k9s` must be added to `kelperSubcommands` in `cmd/kelper/main.go:27`, or the kubectl fast-path swallows it (`error: unknown command "k9s" for "kubectl"`).
2. k9s's cobra root parses `os.Args` directly, so `os.Args` must be rewritten to `["k9s", args...]` before `Execute()`, otherwise k9s sees its own name as a subcommand.
3. The cobra command needs `DisableFlagParsing: true`, or kelper intercepts `--help`/flags meant for k9s.
4. k9s internals live under `internal/` and **cannot be imported** — only `cmd` is reachable. Borrowing k9s logic means copying files with Apache-2.0 attribution, not importing.

---

## File Map

| File | Responsibility |
|---|---|
| `internal/health/finding.go` | `Finding`, `Severity`, `ResourceRef`, `Evidence` types |
| `internal/health/registry.go` | `Check` interface, registry, concurrent runner |
| `internal/health/snapshot.go` | One-shot concurrent fetch of all cluster state |
| `internal/health/capability.go` | Detect metrics-server / nodes-proxy / dynamic access |
| `internal/health/history.go` | Run history ring buffer; new/resolved/flapping diff |
| `internal/health/checks/pods.go` | Pending, unschedulable, init containers, OOMKilled, evicted, terminating |
| `internal/health/checks/restarts.go` | Restart churn inside the kubelet backoff window |
| `internal/health/checks/workloads.go` | Deployment/STS/DaemonSet/Job/CronJob, rewritten |
| `internal/health/checks/replicasets.go` | Stale and orphaned ReplicaSets |
| `internal/health/checks/nodes.go` | Node conditions, cordon, cpu/mem/disk pressure |
| `internal/health/checks/limits.go` | Pod usage vs limits/requests (yellow at risk) |
| `internal/health/checks/pvc.go` | PVC usage vs capacity, pending PVCs |
| `internal/health/checks/certs.go` | Pending CSRs, kubelet cert TTL, apiserver cert expiry |
| `internal/health/checks/services.go` | Services with no ready endpoints |
| `internal/health/checks/custom.go` | Generic CR condition scan via discovery + dynamic |
| `internal/health/sources/metrics.go` | `metrics.k8s.io` client wrapper |
| `internal/health/sources/kubelet.go` | Node proxy `/stats/summary` and `/metrics` |
| `internal/health/sources/discovery.go` | `ServerPreferredResources` + dynamic listing |
| `internal/health/render/table.go` | CLI table + summary renderer |
| `internal/health/dashboard/*.go` | tview health dashboard |
| `internal/k9s/k9s.go` | Embedded k9s launcher |
| `internal/healthcheck/` | **Deleted** — replaced by `internal/health` |

---

## Task 1: Dependency Bump & Module Prep

- [ ] Set `go 1.25.0` / `toolchain go1.25.8` in `go.mod`
- [ ] `go get k8s.io/api@v0.35.3 k8s.io/apimachinery@v0.35.3 k8s.io/client-go@v0.35.3`
- [ ] Add `k8s.io/metrics@v0.35.3`, `github.com/derailed/tview`, `github.com/gdamore/tcell/v2`
- [ ] `go mod tidy`; verify `go build ./... && go vet ./... && go test ./...` all pass
- [ ] Add `-s -w` to `LDFLAGS` in `Makefile` to claw back ~67MB
- [ ] Add `NOTICE` file crediting k9s (Apache 2.0) and note it in `README.md`

**Verification:** clean build, vet, and full test suite on the bumped tree.

## Task 2: Client Refactor — `kubernetes.Interface`

This is the change that unblocks all testing. Currently every package takes a concrete
`*kubernetes.Clientset`, which the fake clientset cannot satisfy — the reason existing
tests only cover the pure `IsUnhealthyPod` function.

- [ ] Change `internal/client.New` to return `kubernetes.Interface`
- [ ] Add `client.NewBundle()` returning a struct of `kubernetes.Interface`, `dynamic.Interface`, `discovery.DiscoveryInterface`, `metricsclient.Interface`, and the `*rest.Config` (needed for node proxy)
- [ ] Change `cmd/kelper/main.go` and every feature package to accept the interface
- [ ] Remove all `os.Exit` calls from library code — return errors instead (currently `healthcheck.go:53` and `:66`)
- [ ] Confirm `images`, `resources`, `volumes`, `get`, `kubeconfig` still build and pass

**Verification:** a test constructing `fake.NewSimpleClientset()` and passing it to a feature package compiles and runs.

## Task 3: Finding Model & Registry

- [ ] `Severity`: `Ok`, `Info`, `Warn`, `Error`, `Critical`. `Warn` is the "at risk / yellow" tier the maintainer asked for on limits and PVC headroom.
- [ ] `Finding{Check, Severity, Ref, Reason, Message, Detail, Evidence, Remediation, Since}`
- [ ] `ResourceRef{Group, Kind, Namespace, Name, Node}`
- [ ] `Evidence` carries correlated Events and metric readings — the "why" behind a symptom
- [ ] `Check` interface: `Name()`, `Description()`, `Requires() []Capability`, `Run(ctx, *Snapshot) ([]Finding, error)`
- [ ] Registry with explicit registration, `--checks` / `--skip` selection, deterministic ordering
- [ ] Runner executes checks concurrently with a bounded worker pool; one check's error never aborts the run — it becomes an `Error`-severity finding against the check itself

**Verification:** unit tests registering fake checks; assert selection, ordering, error isolation.

## Task 4: Snapshot & Capability Detection

- [ ] Define `SnapshotProvider` interface up front — `ListProvider` here, `InformerProvider` in Task 17. Checks never know which one they are running under.
- [ ] `Snapshot` concurrently fetches pods, deployments, replicasets, statefulsets, daemonsets, jobs, cronjobs, nodes, PVCs, services, endpointslices, events, CSRs — **once**, shared by all checks
- [ ] Index events by involved-object UID for O(1) evidence correlation
- [ ] Honor a context timeout and a namespace scope (`""` = all)
- [ ] `Capability` probes: `CapMetrics` (discovery `ServerGroups` contains `metrics.k8s.io`), `CapNodeProxy` (single `SelfSubjectAccessReview` for `nodes/proxy`), `CapDynamic` (discovery reachable)
- [ ] Checks whose `Requires()` are unmet are skipped and recorded in `Snapshot.Degraded`
- [ ] Renderers print one line: `metrics unavailable: N checks skipped` — never an error

**Verification:** fake clientset tests for the fetch; capability tests with a discovery stub that omits `metrics.k8s.io`.

## Task 5: Pod Checks — the current blind spots

Every item here is invisible to today's `IsUnhealthyPod`, which only inspects
`ContainerStatuses` and so reports a pod with **zero** container statuses as healthy.

- [ ] **Pending / unschedulable** — `PodPending` with no container statuses; attach the `FailedScheduling` event as evidence (today: reported healthy)
- [ ] **Init containers** — scan `InitContainerStatuses`; init `ImagePullBackOff` is currently invisible
- [ ] **Failed / Evicted** — `PodFailed` phase, `Reason == "Evicted"`
- [ ] **OOMKilled** — `LastTerminationState.Terminated.Reason`, even when currently Running
- [ ] **Stuck Terminating** — `DeletionTimestamp` older than grace period + threshold
- [ ] **ImagePull / CreateContainerConfigError** — distinguish from generic not-ready
- [ ] Preserve today's behavior: `PodSucceeded` is healthy

**Verification:** table-driven tests, one crafted pod per condition, including a Pending pod with no container statuses.

## Task 6: Restart Churn Check

The maintainer's "pods still inside their restart loop" ask. kubelet resets its
CrashLoopBackOff timer after ~10 minutes of stable running, so a pod that restarted
recently and is *currently* Running is the one about to crash again — precisely what a
plain Ready check misses.

- [ ] Flag pods where `RestartCount > 0` and `LastTerminationState.Terminated.FinishedAt` is within the backoff window (default 10m, `--restart-window` flag)
- [ ] Severity by restart rate: recent single restart → `Warn`; sustained churn → `Error`
- [ ] Report restart count, time since last restart, last exit code and reason
- [ ] Explicitly flag currently-Running pods so they are not filtered out as healthy

**Verification:** tests for currently-Running-but-recently-restarted, in-backoff, and long-stable-with-old-restarts (must NOT flag).

## Task 7: Workload Checks — rewrite and audit

Audit of the existing logic found real defects, all fixed here:

- [ ] **Dead code:** the CronJob `List` at `healthcheck.go:188` discards its result entirely — replace with real checks
- [ ] **Failed Jobs are flagged forever** with no time window — add `--job-window` (default 24h)
- [ ] **ReplicaSets are never checked at all**
- [ ] **Deployment** — add `Available=False`, `Progressing=False` (ProgressDeadlineExceeded), `ReplicaFailure=True`, and `observedGeneration < generation` (stuck rollout) on top of the existing replica shortfall
- [ ] **StatefulSet** — add `currentRevision != updateRevision` (stuck rollout) and `observedGeneration`
- [ ] **DaemonSet** — add `numberUnavailable`, `numberMisscheduled`, `updatedNumberScheduled` shortfall
- [ ] **Job** — add `activeDeadlineSeconds` overrun and approaching `backoffLimit`
- [ ] **CronJob** — suspended, `lastScheduleTime` stale relative to schedule, no successful run within N periods
- [ ] Nil-guard `Spec.Replicas` throughout (today's code silently `continue`s past it)

**Verification:** table-driven tests per workload kind, including regression tests for each defect above.

## Task 8: Stale ReplicaSet Check

- [ ] Resolve each RS's owner Deployment via `OwnerReferences`
- [ ] Compare RS `deployment.kubernetes.io/revision` against the Deployment's current revision
- [ ] Flag non-current RS with `replicas > 0` — a stale RS still holding pods, meaning a rollout that never finished draining
- [ ] Flag orphaned RS (owner Deployment absent) with `replicas > 0`
- [ ] Ignore the normal case: non-current RS scaled to 0 (retained rollout history)

**Verification:** fixtures for current RS, stale-with-pods, orphaned, and scaled-to-zero history (must NOT flag).

## Task 9: Node Health Check

- [ ] Conditions: `Ready != True`, `MemoryPressure`, `DiskPressure`, `PIDPressure`, `NetworkUnavailable`
- [ ] `Unschedulable` (cordoned) → `Warn`
- [ ] Allocatable vs sum of pod requests — overcommit detection (no metrics needed)
- [ ] With `CapMetrics`: actual node cpu/memory utilization, `Warn` above threshold
- [ ] With `CapNodeProxy`: filesystem and imagefs usage from `/stats/summary`
- [ ] Kubelet version skew across nodes → `Info`

**Verification:** node fixtures per condition; metrics-absent path must degrade, not error.

## Task 10: Limits & Requests Check (metrics-dependent)

- [ ] Pull `PodMetrics` from `metrics.k8s.io`
- [ ] Compare usage against each container's limit: `Warn` above threshold (default 85%, `--limit-threshold`), `Error` at/over 100%
- [ ] Memory near limit is the leading OOMKill predictor — pair with Task 5's OOMKilled evidence when both fire on one pod
- [ ] CPU near limit indicates throttling risk
- [ ] Separate `Info` finding: containers with **no** limits or requests set
- [ ] Skip cleanly without `CapMetrics`

**Verification:** fake metrics client fixtures at 50%/90%/105% of limit; absent-metrics path skips.

## Task 11: PVC Usage Check (kubelet-dependent)

PVC utilization is **not** exposed by metrics-server. It comes from the kubelet Summary
API through the node proxy.

- [ ] `GET /api/v1/nodes/<node>/proxy/stats/summary`, read `pods[].volume[]` `usedBytes` / `capacityBytes`
- [ ] Map volume entries back to PVC names via `pvcRef`
- [ ] `Warn` above threshold (default 85%, `--pvc-threshold`), `Error` above 95%
- [ ] Also report `inodesUsed` / `inodesFree` exhaustion — a silent, frequently-missed failure
- [ ] Separate check, no proxy needed: PVCs stuck `Pending` (unbound)
- [ ] Requires `CapNodeProxy`; skip cleanly and count toward the degraded line
- [ ] Query nodes concurrently with a bounded pool; a single unreachable kubelet must not fail the run

**Verification:** stubbed summary JSON at varying fill levels; unreachable-node path degrades gracefully.

## Task 12: Certificate Check

> **Corrected after probing the live cluster.** The originally planned source,
> `kubelet_certificate_manager_*_ttl_seconds`, **does not exist on RKE2** — that cluster
> ships certs as managed files rather than through the kubelet certificate manager, so
> those gauges are never registered. The node `/metrics` endpoint exposes only
> `apiserver_client_certificate_expiration_seconds`, a histogram that cannot answer
> "when does *this* node's cert expire".
>
> Use **TLS handshake** instead. Verified working against the live cluster for all three
> sources, and it is distro-independent — no metrics, no node proxy, no RBAC beyond
> network reach.

- [ ] Pending CSRs awaiting approval (kelper already creates CSRs in `internal/kubeconfig`)
- [ ] **API-server cert** — TLS handshake against the endpoint, read peer cert `NotAfter`. Reuse the dialing approach in `internal/failover/failover.go` (verified: `CN=kube-apiserver`, expires 2027-08-21)
- [ ] **Kubelet cert** — handshake each node on `:10250` (verified: `CN=<nodename>`, expires 2027-08-21)
- [ ] **Client cert** — parse `client-certificate-data` out of the active kubeconfig; this is the credential that locks the user out when it lapses (verified: `O=system:masters, CN=system:admin`)
- [ ] `Warn` under 30 days remaining, `Error` under 7
- [ ] Use `InsecureSkipVerify` for the handshake — reading `NotAfter` must not require a valid chain, and self-signed cluster CAs are the norm
- [ ] Bounded concurrency across nodes; an unreachable kubelet degrades that node only

**Verification:** handshake against a local TLS server with a short-dated cert; unreachable-endpoint path degrades.

## Task 13: Services Without Endpoints

- [ ] Flag `ClusterIP`/`LoadBalancer` Services whose EndpointSlices contain no ready addresses
- [ ] Skip headless services, ExternalName, and services with no selector (manually managed)
- [ ] Correlate against the selector's pods to name the cause when it is a pod-side failure

**Verification:** fixtures for backed, unbacked, headless, and selectorless services.

## Task 14: Custom Resource Health Scan

Answers the maintainer's question directly: `ServerPreferredResources()` returns every
API resource including CRDs **and cluster-scoped ones** — the latter being exactly what
today's namespaced-only queries miss. There is no way to avoid the per-resource list
calls, but they can be bounded and made concurrent.

- [ ] Discovery via `ServerPreferredResources()`; filter to resources supporting `list`
- [ ] Include cluster-scoped resources explicitly (the current gap)
- [ ] Skip built-in kinds already covered by dedicated checks, and skip high-cardinality noise (events, endpoints, leases, secrets, configmaps)
- [ ] List each remaining resource via the dynamic client, bounded worker pool, per-resource timeout
- [ ] Generic condition evaluation over `status.conditions`: `Ready`/`Available`/`Healthy`/`Synced` = `False`, or `Degraded`/`Failed` = `True`. This one check covers cert-manager, Istio, ArgoCD, Flux, and anything else following the convention.
- [ ] Fall back to `status.phase` when no conditions exist
- [ ] `--crds=false` to skip, `--crd-timeout` to bound; RBAC-denied resources are skipped silently
- [ ] Cache discovery results for the process lifetime

**Verification:** fake dynamic client with synthetic CRs — conditions true/false/absent, cluster-scoped and namespaced; assert RBAC denial does not fail the run.

## Task 15: CLI Renderer & Command

- [ ] `internal/health/render/table.go` — findings grouped by severity, then namespace/kind
- [ ] Severity color: Critical/Error red, Warn yellow, Info dim, Ok green
- [ ] Summary line with per-severity counts plus the single degraded-capabilities line
- [ ] `--detail` expands evidence (events, metric readings, remediation)
- [ ] Flags: `-n`, `-A`, `--checks`, `--skip`, `--severity`, `--detail`, `--timeout`, `--crds`, thresholds from Tasks 10/11, `--restart-window`, `--job-window`
- [ ] Exit code driven by highest severity found
- [ ] Rewire `healthcheck.Command` to the new engine; keep the `health` alias
- [ ] Delete `internal/healthcheck/`; update `README.md` with new output samples

**Verification:** golden-file tests over renderer output for a fixed finding set.

## Task 16: Watch Mode & History

- [ ] `internal/health/history.go` — ring buffer of runs keyed by finding identity (`check + ref + reason`)
- [ ] Diff consecutive runs into **new**, **resolved**, **flapping** (N transitions in a window), and **persistent** with age
- [ ] `kelper healthcheck --watch [--interval 30s]` — long-lived, prints only deltas
- [ ] `Since` on each finding tracks first-observed time across runs
- [ ] Clean shutdown on SIGINT
- [ ] History is in-memory and shared with the dashboard, which renders it as a timeline

**Verification:** feed a scripted sequence of finding sets; assert correct new/resolved/flapping classification.

## Task 17: Living Health Dashboard (tview)

Full TUI refactor. The existing bubbletea `tui.go` is deleted — it duplicates pod logic
by hand, shows pods only, and never renders workloads.

**This is a continuously-updating dashboard, not a repeating snapshot.** The distinction
is architectural, not cosmetic: the dashboard must not re-`List` the whole cluster on a
timer. It maintains a warm, watch-fed cache and re-evaluates only what changed.

### Two snapshot providers behind one interface

The `Check` implementations stay pure functions over a `Snapshot` and are shared
unchanged between both modes. Only the *provider* differs:

```go
type SnapshotProvider interface {
    Snapshot(ctx context.Context) (*Snapshot, error)
}
```

- [ ] `ListProvider` — one-shot concurrent `List` (Task 4). Used by the CLI: fetch, render, exit.
- [ ] `InformerProvider` — `SharedInformerFactory` over pods, deployments, replicasets, statefulsets, daemonsets, jobs, cronjobs, nodes, services, endpointslices, events, PVCs. Builds a `Snapshot` from warm listers with **zero API calls** after initial sync.
- [ ] Wait for `WaitForCacheSync` before the first render; show a loading state meanwhile.

### Update loop

- [ ] Informer event handlers (add/update/delete) mark the affected checks dirty and coalesce updates through a debounce (~250ms) so a rollout storm doesn't cause a repaint storm.
- [ ] Re-run only dirty checks; merge results into the live finding set.
- [ ] **Non-watchable sources poll on their own cadence** — the metrics API, kubelet `/stats/summary`, and TLS cert handshakes cannot be watched. Independent tickers: metrics ~30s, volume stats ~60s, certs ~1h. Each runs in its own goroutine and never blocks the UI.
- [ ] All UI mutation goes through `app.QueueUpdateDraw` — tview is not goroutine-safe.
- [ ] Findings carry stable identity (`check + ref + reason`) so the table doesn't reorder or lose selection under the user on refresh.

### Grafana-style panels

- [ ] **Header:** cluster/context, live per-severity counters, cache-sync state, degraded-capabilities notice
- [ ] **Gauges:** node CPU/memory/disk utilization, updated from the metrics ticker
- [ ] **Sparklines:** severity counts over time, restart rate, pod-ready ratio — fed from the Task 16 history ring buffer
- [ ] **Findings table:** severity-colored, sortable (severity/namespace/kind/age), incremental-search filter, new findings briefly highlighted and resolved ones struck through before removal
- [ ] **Detail pane:** full finding — evidence, correlated events, remediation, per-finding history timeline
- [ ] **Event stream pane:** live cluster warnings, tailed from the events informer
- [ ] k9s's `internal/tchart` (Apache 2.0) has usable sparkline/gauge widgets — copy with attribution rather than writing from scratch; it cannot be imported (`internal/`)

### Interaction

- [ ] k9s-like keybindings: `:` command, `/` filter, `esc` back, `s` sort, `d` describe, `l` logs, `e` events, `?` help, `q` quit
- [ ] `Ctrl+C` and `q` both exit cleanly — cancel the context, stop informers, restore the terminal
- [ ] Drill-down: pod logs, describe resource, delete/restart pod (with confirmation)
- [ ] Pause/resume live updates (`p`) so a user can read a finding without it moving
- [ ] Bare `kelper healthcheck` launches this dashboard, preserving today's entry-point behavior

**Verification:** state-model unit tests (filtering, sorting, selection stability, debounce coalescing) separated from tview rendering; informer-provider tests against a fake clientset with scripted watch events; manual smoke against the live cluster with induced failures.

## Task 18: Embed k9s

- [ ] `internal/k9s/k9s.go` — rewrite `os.Args` to `["k9s", args...]`, then `k9scmd.Execute()`
- [ ] Register the cobra command with `DisableFlagParsing: true` so flags reach k9s
- [ ] Add `"k9s": true` to `kelperSubcommands` in `cmd/kelper/main.go`
- [ ] Set k9s version vars via `-X` ldflags in the `Makefile`
- [ ] Document the binary-size increase in `README.md`

**Verification:** `kelper k9s version`, `kelper k9s --help`, and a live `kelper k9s` launch.

## Task 19: Integration & Docs

- [ ] Full `go build ./... && go vet ./... && go test ./...`
- [ ] Smoke-test every command against a live cluster
- [ ] `README.md`: rewrite the healthcheck section, document all checks and flags, add dashboard screenshots
- [ ] Document required RBAC (`nodes/proxy`, `metrics.k8s.io`, CR list) and exactly which checks degrade without each
- [ ] Update shell completions for the new flags

---

## Test Cluster

`KUBECONFIG=~/git/gsp/gsp-engine/.gsp/kubeconfig` — 3-node RKE2, **v1.35.5**, 12
namespaces, 109 pods, 105 listable API resources, 45 CRDs, 55 ReplicaSets (48 active,
7 scaled to zero), 17 Services.

The server being v1.35 **validates the Task 1 dependency bump independently**: k8s.io
v0.35.3 matches the server exactly, where today's v0.29 client is six minors skewed.

Capabilities probed — **all present, so no check degrades here**:

| Capability | Status |
|---|---|
| metrics-server (`v1beta1.metrics.k8s.io`) | Available (`rke2-metrics-server`) |
| `nodes/proxy` RBAC | Permitted |
| Cluster-admin | Yes |
| Kubelet `/stats/summary` | Reachable and parsing |

Because everything is available, the degraded-path code (Task 4) **cannot be exercised
here** — cover it with capability stubs in unit tests, not on this cluster.

### Testing strategy

The cluster is **entirely healthy**: no non-Running pods, no restart churn, no failed
jobs. That makes it an excellent **false-positive baseline** — a correct implementation
should report almost nothing against it, and anything it does report deserves scrutiny.

True-positive coverage needs induced failures in a scratch namespace (`kelper-health-test`),
torn down afterward:

- [ ] Bad image tag → `ImagePullBackOff`
- [ ] Container exiting nonzero → `CrashLoopBackOff` and restart-window churn (Task 6)
- [ ] Memory limit far below usage → `OOMKilled` (Tasks 5, 10)
- [ ] Impossible `nodeSelector` or oversized request → Pending/unschedulable (Task 5) — the case today's code reports as *healthy*
- [ ] Deployment with a failing readiness probe → `Progressing=False` (Task 7)
- [ ] Rollout to a bad image, left mid-flight → stale ReplicaSet with pods (Task 8)
- [ ] Service with a selector matching nothing → no endpoints (Task 13)
- [ ] **PVC checks (Task 11) cannot be tested as-is — the cluster has zero PVCs.** Create a bound PVC and fill it to exercise the usage path, plus an unbound PVC for the Pending path.

## Open Risks

| Risk | Mitigation |
|---|---|
| Binary grows 54MB → 168MB (stripped) | Accepted by maintainer. `-s -w` added; a build tag to exclude k9s is a fallback if it becomes painful |
| CR scan cost | 105 listable resources on the test cluster — bounded worker pool, per-resource timeout, skip-list, `--crds=false` |
| Informer memory footprint on large clusters | Field/label selectors where possible; `--namespace` scoping shrinks the watch set; dashboard only |
| tview is not goroutine-safe | All updates funnel through `QueueUpdateDraw`; poller goroutines never touch widgets directly |
| Degraded paths unexercised on the test cluster | Capability stubs in unit tests — everything is available live |
| k9s upstream drift | Only the public `cmd` API is imported — a narrow, stable surface |
| Two TUI frameworks in one binary | tview arrives with k9s regardless; the bubbletea kubeconfig wizard is left alone for now |
