// Package engine wires kelper's health clients, capability detection and check
// registry together into repeatable runs. It lives apart from the health
// package so that checks can depend on the finding model without a cycle.
package engine

import (
	"context"
	"time"

	"github.com/jthunderbird/kelper/internal/client"
	"github.com/jthunderbird/kelper/internal/health"
	"github.com/jthunderbird/kelper/internal/health/checks"
	"github.com/jthunderbird/kelper/internal/health/sources"
	corev1 "k8s.io/api/core/v1"
)

// Options configures a health run.
type Options struct {
	Namespace string
	// Include and Exclude select checks by name; empty Include means all.
	Include []string
	Exclude []string
	// MinSeverity filters findings below this level out of the result.
	MinSeverity health.Severity

	// Thresholds.
	LimitThreshold float64
	PVCThreshold   float64
	NodeThreshold  float64
	RestartWindow  time.Duration
	JobWindow      time.Duration

	// ScanCRDs enables the custom-resource scan.
	ScanCRDs bool
	// CRDTimeout bounds each custom-resource list call.
	CRDTimeout time.Duration
	// ReportMissingLimits enables informational findings for containers with no
	// resource constraints.
	ReportMissingLimits bool
	// Timeout bounds a whole run.
	Timeout time.Duration
}

// withDefaults fills unset options with sensible values.
func (o Options) withDefaults() Options {
	if o.LimitThreshold == 0 {
		o.LimitThreshold = 85
	}
	if o.PVCThreshold == 0 {
		o.PVCThreshold = 85
	}
	if o.NodeThreshold == 0 {
		o.NodeThreshold = 85
	}
	if o.RestartWindow == 0 {
		o.RestartWindow = checks.DefaultRestartWindow
	}
	if o.JobWindow == 0 {
		o.JobWindow = checks.DefaultJobWindow
	}
	if o.CRDTimeout == 0 {
		o.CRDTimeout = 10 * time.Second
	}
	if o.Timeout == 0 {
		o.Timeout = 2 * time.Minute
	}
	return o
}

// Engine ties clients, capabilities and checks together for repeated runs. It
// is safe to run more than once; the dashboard and watch modes do exactly that.
type Engine struct {
	bundle   *client.Bundle
	opts     Options
	registry *health.Registry
	caps     *health.Capabilities
}

// NewEngine builds an engine, detecting cluster capabilities once up front.
func NewEngine(ctx context.Context, b *client.Bundle, opts Options) *Engine {
	opts = opts.withDefaults()
	caps := health.DetectCapabilities(ctx, b.Kube, b.Discovery)
	return &Engine{
		bundle:   b,
		opts:     opts,
		registry: DefaultRegistry(opts),
		caps:     caps,
	}
}

// Registry exposes the engine's checks, for help output and selection.
func (e *Engine) Registry() *health.Registry { return e.registry }

// Capabilities exposes what this cluster supports.
func (e *Engine) Capabilities() *health.Capabilities { return e.caps }

// DefaultRegistry builds the standard check set, configured from opts.
func DefaultRegistry(opts Options) *health.Registry {
	opts = opts.withDefaults()
	r := health.NewRegistry()
	r.Register(checks.Pods{})
	r.Register(checks.Restarts{Window: opts.RestartWindow})
	r.Register(checks.Workloads{JobWindow: opts.JobWindow})
	r.Register(checks.ReplicaSets{})
	r.Register(checks.Nodes{UtilizationThreshold: opts.NodeThreshold})
	r.Register(checks.Limits{
		Threshold:     opts.LimitThreshold,
		ReportMissing: opts.ReportMissingLimits,
	})
	r.Register(checks.Volumes{Threshold: opts.PVCThreshold})
	r.Register(checks.Certificates{})
	r.Register(checks.Services{})
	r.Register(checks.CustomResources{})
	return r
}

// Run gathers a snapshot and evaluates the selected checks.
func (e *Engine) Run(ctx context.Context) (health.Result, *health.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()

	selected, err := e.registry.Select(e.opts.Include, e.opts.Exclude)
	if err != nil {
		return health.Result{}, nil, err
	}

	provider := &health.ListProvider{
		Kube:      e.bundle.Kube,
		Namespace: e.opts.Namespace,
		Caps:      e.caps,
	}
	snap, err := provider.Snapshot(ctx)
	if err != nil {
		return health.Result{}, nil, err
	}
	e.enrich(ctx, snap)

	res := health.Run(ctx, selected, snap)
	res.Findings = filterSeverity(res.Findings, e.opts.MinSeverity)
	return res, snap, nil
}

// enrich adds the optional data sources to a snapshot. Each is best-effort:
// a source that cannot be reached simply leaves its fields empty, and the
// checks that need it are skipped by capability gating.
func (e *Engine) enrich(ctx context.Context, s *health.Snapshot) {
	if e.caps.Metrics {
		s.PodMetrics, s.NodeMetrics = sources.FetchMetrics(ctx, e.bundle.Metrics, e.opts.Namespace)
	}

	if e.caps.NodeProxy && len(s.Nodes) > 0 {
		names := make([]string, 0, len(s.Nodes))
		for i := range s.Nodes {
			names = append(names, s.Nodes[i].Name)
		}
		vols, fs := sources.FetchVolumeStats(ctx, e.bundle.Kube, names)
		for k, v := range vols {
			s.VolumeStats[k] = health.VolumeStat{
				Namespace: v.Namespace, PVC: v.PVC, Node: v.Node,
				UsedBytes: v.UsedBytes, CapacityBytes: v.CapacityBytes,
				InodesUsed: v.InodesUsed, InodesFree: v.InodesFree,
			}
		}
		for k, v := range fs {
			s.NodeFilesystems[k] = health.NodeFsStat{
				Node: v.Node, UsedBytes: v.UsedBytes, CapacityBytes: v.CapacityBytes,
				InodesUsed: v.InodesUsed, InodesFree: v.InodesFree,
			}
		}
	}

	// Certificate expiry by TLS handshake, which needs no cluster capability
	// beyond network reach to the endpoints.
	addrs := map[string]string{}
	for i := range s.Nodes {
		if a := internalAddress(&s.Nodes[i]); a != "" {
			addrs[s.Nodes[i].Name] = a
		}
	}
	for _, c := range sources.FetchCerts(ctx, e.bundle.Config, addrs) {
		s.CertExpiry[c.Endpoint] = c.NotAfter
	}

	if e.opts.ScanCRDs && e.caps.Dynamic {
		for _, cr := range sources.ScanCustomResources(ctx, e.bundle.Discovery, e.bundle.Dynamic, e.opts.Namespace, e.opts.CRDTimeout) {
			s.Custom = append(s.Custom, health.CustomResource{
				Group: cr.Group, Kind: cr.Kind,
				Namespace: cr.Namespace, Name: cr.Name,
				Ready: cr.Ready, Reason: cr.Reason,
				Message: cr.Message, Phase: cr.Phase,
			})
		}
	}
}

// internalAddress returns a node's InternalIP, preferred over any external
// address because the kubelet listens on the internal network.
func internalAddress(n *corev1.Node) string {
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeHostName {
			return a.Address
		}
	}
	return ""
}

// filterSeverity drops findings below min.
func filterSeverity(fs []health.Finding, min health.Severity) []health.Finding {
	if min == health.SeverityOk {
		return fs
	}
	out := fs[:0]
	for _, f := range fs {
		if f.Severity >= min {
			out = append(out, f)
		}
	}
	return out
}
