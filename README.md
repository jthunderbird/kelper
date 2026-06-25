# kelper

A small Go wrapper around `kubectl` that makes everyday cluster work nicer:

- **Decodes secrets** automatically on `-o yaml` output.
- **Cleans up YAML** by stripping the noise Kubernetes injects (`uid`,
  `creationTimestamp`, `status`), so output is paste-ready for a manifest.
- **Lists pods** with their init containers, containers, and images.
- **Client-side api-server load balancing** — point a single context at a
  comma-delimited list of api-server endpoints and `kelper` will fail over to the
  next live one automatically.

## Installing

### Pre-built binaries (recommended)

Grab the latest binary for your OS/arch from the
[Releases](https://github.com/jthunderbird/kelper/releases) page, then:

```bash
chmod +x kelper-linux-amd64
sudo mv kelper-linux-amd64 /usr/local/bin/kelper
alias k=kelper   # optional
k --help
```

### Container image (GHCR)

```bash
docker pull ghcr.io/jthunderbird/kelper:latest
docker run --rm -v ~/.kube:/root/.kube ghcr.io/jthunderbird/kelper:latest get pods -A
```

The image bundles `kubectl`, so it works standalone.

### From source

```bash
git clone https://github.com/jthunderbird/kelper.git
cd kelper
go build -o kelper ./cmd/kelper
```

`kelper` shells out to `kubectl`, so `kubectl` must be on your `PATH` (the
container image already includes it).

## Usage

`kelper` passes any arguments straight through to `kubectl`, intercepting the
output to decode secrets and tidy YAML:

```bash
kelper <any kubectl args>
kelper -list-pods -namespace <ns>     # list pods + their images
kelper -kubeconfig <path> <args>      # use a specific kubeconfig
```

| Flag           | Default     | Description                                            |
| -------------- | ----------- | ------------------------------------------------------ |
| `-kubeconfig`  | discovery\* | Path to the kubeconfig file.                           |
| `-namespace`   | `default`   | Namespace for `-list-pods`.                            |
| `-list-pods`   | `false`     | List pods with their init containers and containers.   |

\* When unset, the standard `KUBECONFIG` env var / `~/.kube/config` discovery
is used.

## In action

### Secrets — auto-decoded

Any `get secret ... -o yaml` has its `data` base64-decoded in place:

```bash
$ kelper get secret -n kiali grafana-auth -o yaml
apiVersion: v1
kind: Secret
metadata:
  name: grafana-auth
  namespace: kiali
data:
  password: prom-operator
```

### `-o yaml` — cleaned up

For any non-secret `-o yaml`, `kelper` removes the auto-mutated fields
(`metadata.uid`, `metadata.creationTimestamp`, `status`) so the result is ready
to drop into a YAML file:

```bash
$ kelper get po -n flux-system helm-controller-678f5576df-g7scx -o yaml
apiVersion: v1
kind: Pod
metadata:
  annotations:
    prometheus.io/port: "8080"
    prometheus.io/scrape: "true"
  labels:
    app: helm-controller
  name: helm-controller-678f5576df-g7scx
  namespace: flux-system
spec:
  containers:
    - image: docker.io/fluxcd/helm-controller:v1.4.0
      name: manager
      ...
```

### `-list-pods` — pods and their images

```bash
$ kelper -list-pods -namespace kyverno
Pod: kyverno-admission-controller-5d8986c8b6-2g7gr, Namespace: kyverno
Init Containers:
  Name: kyverno-pre, Image: registry1.dso.mil/ironbank/opensource/kyverno/kyvernopre:v1.13.4
Containers:
  Name: kyverno, Image: registry1.dso.mil/ironbank/opensource/kyverno:v1.13.4
```

## API-server load balancing

`kubectl` accepts one — and only one — `server:` per cluster in a kubeconfig.
If that endpoint is down, you are stuck. `kelper` lifts that limit: list multiple
api-server endpoints, comma-delimited, in the `server:` field, and `kelper`
probes them in order and uses the first one that is reachable.

### Configure it

```yaml
# ~/.kube/config
apiVersion: v1
kind: Config
clusters:
  - name: prod
    cluster:
      certificate-authority-data: <ca>
      # comma-delimited list of api-server endpoints
      server: https://10.0.0.1:6443,https://10.0.0.2:6443,https://10.0.0.3:6443
contexts:
  - name: prod
    context:
      cluster: prod
      user: prod-admin
current-context: prod
users:
  - name: prod-admin
    user:
      client-certificate-data: <cert>
      client-key-data: <key>
```

### How it behaves

On each invocation `kelper`:

1. Reads the current context's `server` field and splits it on commas.
2. Probes each endpoint in order (a 2s TCP dial).
3. Logs a line to stdout for every endpoint that is down and moves on.
4. Uses the first reachable endpoint by rewriting a temporary single-server
   kubeconfig and handing it to `kubectl` / the client.
5. If every endpoint is exhausted, exits non-zero with a `not connected` error.

Example, first endpoint down:

```bash
$ kelper get pods -n flux-system
api-server https://10.0.0.1:6443 unreachable (dial tcp 10.0.0.1:6443: i/o timeout); trying next endpoint...
api-server https://10.0.0.2:6443 reachable; using it
NAME                                READY   STATUS    RESTARTS   AGE
helm-controller-678f5576df-g7scx    1/1     Running   0          20h
```

All endpoints down:

```bash
$ kelper get pods
api-server https://10.0.0.1:6443 unreachable (dial tcp 10.0.0.1:6443: i/o timeout); trying next endpoint...
api-server https://10.0.0.2:6443 unreachable (dial tcp 10.0.0.2:6443: i/o timeout); trying next endpoint...
api-server https://10.0.0.3:6443 unreachable (dial tcp 10.0.0.3:6443: i/o timeout); trying next endpoint...
not connected: all 3 api-server endpoints unreachable
```

A single (non-delimited) `server:` value behaves exactly as before — no probing,
no temp kubeconfig.

## Releases & images

Every push to `main` runs the [`release`](.github/workflows/release.yml)
workflow, which:

- cross-compiles `kelper` for linux/macOS (amd64 + arm64) and windows (amd64) and
  attaches them to a GitHub Release, and
- builds and pushes the container image to
  `ghcr.io/jthunderbird/kelper` (`:latest`, the release tag, and the commit SHA).

## License

See [LICENSE](LICENSE).
