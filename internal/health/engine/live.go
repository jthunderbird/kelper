package engine

import (
	"context"
	"sync"
	"time"

	"github.com/jthunderbird/kelper/internal/health"
	"github.com/jthunderbird/kelper/internal/health/sources"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

// Cadences for the sources that cannot be watched. Metrics and volume stats are
// polled endpoints, and certificate expiry moves slowly enough to check rarely.
const (
	metricsInterval = 30 * time.Second
	volumeInterval  = 60 * time.Second
	certInterval    = time.Hour
	// debounce coalesces bursts of informer events so a rollout does not cause
	// a repaint per pod.
	debounce = 250 * time.Millisecond
)

// Live maintains a continuously updated finding set backed by informers. Watch
// events drive re-evaluation; the pollable sources refresh on their own timers.
// Nothing here touches the UI: consumers subscribe via Updates.
type Live struct {
	engine   *Engine
	provider *health.InformerProvider
	history  *health.History

	mu       sync.RWMutex
	result   health.Result
	snapshot *health.Snapshot
	// extras holds the most recent poll results, merged into every snapshot so
	// a watch-driven refresh does not lose metric-derived findings.
	extras extras

	updates chan struct{}
}

// extras is the slow-moving data gathered by the pollers.
type extras struct {
	podMetrics  []metricsv1beta1.PodMetrics
	nodeMetrics []metricsv1beta1.NodeMetrics
	volumes     map[string]health.VolumeStat
	nodeFs      map[string]health.NodeFsStat
	certs       map[string]time.Time
	custom      []health.CustomResource
}

// applyExtras merges polled data into a freshly built snapshot, so findings
// derived from metrics and kubelet stats survive a watch-driven refresh.
func applyExtras(s *health.Snapshot, ex extras) {
	s.PodMetrics, s.NodeMetrics = ex.podMetrics, ex.nodeMetrics
	s.Custom = ex.custom
	for k, v := range ex.volumes {
		s.VolumeStats[k] = v
	}
	for k, v := range ex.nodeFs {
		s.NodeFilesystems[k] = v
	}
	for k, v := range ex.certs {
		s.CertExpiry[k] = v
	}
}

// NewLive starts informers and returns a live engine ready to Run.
func NewLive(ctx context.Context, e *Engine) (*Live, error) {
	p, err := health.NewInformerProvider(ctx, e.bundle.Kube, e.opts.Namespace, e.caps)
	if err != nil {
		return nil, err
	}
	return &Live{
		engine:   e,
		provider: p,
		history:  health.NewHistory(),
		updates:  make(chan struct{}, 1),
		extras: extras{
			volumes: map[string]health.VolumeStat{},
			nodeFs:  map[string]health.NodeFsStat{},
			certs:   map[string]time.Time{},
		},
	}, nil
}

// Updates signals that a new result is available. It is coalescing: a consumer
// that falls behind sees one wake-up rather than a backlog.
func (l *Live) Updates() <-chan struct{} { return l.updates }

// Result returns the most recent findings.
func (l *Live) Result() health.Result {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.result
}

// Snapshot returns the most recent cluster snapshot.
func (l *Live) Snapshot() *health.Snapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.snapshot
}

// History exposes the trend buffer for sparkline rendering.
func (l *Live) History() *health.History { return l.history }

// Capabilities exposes what the cluster supports.
func (l *Live) Capabilities() *health.Capabilities { return l.engine.caps }

// Start runs the update loops until ctx is cancelled. It evaluates once
// immediately so the UI has content before the first event arrives.
func (l *Live) Start(ctx context.Context) {
	l.refreshPollers(ctx)
	l.evaluate(ctx)

	go l.watchLoop(ctx)
	go l.pollLoop(ctx)
}

// watchLoop re-evaluates when informers report a change, debounced so that a
// burst of events produces a single evaluation.
func (l *Live) watchLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.provider.Changed():
		}

		// Collapse a burst: wait for quiet before evaluating.
		timer := time.NewTimer(debounce)
		for quiet := false; !quiet; {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-l.provider.Changed():
				if !timer.Stop() {
					<-timer.C
				}
				timer.Reset(debounce)
			case <-timer.C:
				quiet = true
			}
		}
		l.evaluate(ctx)
	}
}

// pollLoop refreshes the sources that cannot be watched, each on its own
// cadence, then re-evaluates so their findings stay current.
func (l *Live) pollLoop(ctx context.Context) {
	metricsTick := time.NewTicker(metricsInterval)
	volumeTick := time.NewTicker(volumeInterval)
	certTick := time.NewTicker(certInterval)
	defer metricsTick.Stop()
	defer volumeTick.Stop()
	defer certTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-metricsTick.C:
			l.refreshMetrics(ctx)
		case <-volumeTick.C:
			l.refreshVolumes(ctx)
		case <-certTick.C:
			l.refreshCerts(ctx)
		}
		l.evaluate(ctx)
	}
}

// refreshPollers gathers every pollable source once, at startup.
func (l *Live) refreshPollers(ctx context.Context) {
	l.refreshMetrics(ctx)
	l.refreshVolumes(ctx)
	l.refreshCerts(ctx)
	l.refreshCustom(ctx)
}

func (l *Live) refreshMetrics(ctx context.Context) {
	if !l.engine.caps.Metrics {
		return
	}
	pods, nodes := sources.FetchMetrics(ctx, l.engine.bundle.Metrics, l.engine.opts.Namespace)
	l.mu.Lock()
	l.extras.podMetrics, l.extras.nodeMetrics = pods, nodes
	l.mu.Unlock()
}

func (l *Live) refreshVolumes(ctx context.Context) {
	if !l.engine.caps.NodeProxy {
		return
	}
	snap := l.Snapshot()
	if snap == nil || len(snap.Nodes) == 0 {
		return
	}
	names := make([]string, 0, len(snap.Nodes))
	for i := range snap.Nodes {
		names = append(names, snap.Nodes[i].Name)
	}
	vols, fs := sources.FetchVolumeStats(ctx, l.engine.bundle.Kube, names)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.extras.volumes = map[string]health.VolumeStat{}
	for k, v := range vols {
		l.extras.volumes[k] = health.VolumeStat{
			Namespace: v.Namespace, PVC: v.PVC, Node: v.Node,
			UsedBytes: v.UsedBytes, CapacityBytes: v.CapacityBytes,
			InodesUsed: v.InodesUsed, InodesFree: v.InodesFree,
		}
	}
	l.extras.nodeFs = map[string]health.NodeFsStat{}
	for k, v := range fs {
		l.extras.nodeFs[k] = health.NodeFsStat{
			Node: v.Node, UsedBytes: v.UsedBytes, CapacityBytes: v.CapacityBytes,
			InodesUsed: v.InodesUsed, InodesFree: v.InodesFree,
		}
	}
}

func (l *Live) refreshCerts(ctx context.Context) {
	snap := l.Snapshot()
	addrs := map[string]string{}
	if snap != nil {
		for i := range snap.Nodes {
			if a := internalAddress(&snap.Nodes[i]); a != "" {
				addrs[snap.Nodes[i].Name] = a
			}
		}
	}
	certs := sources.FetchCerts(ctx, l.engine.bundle.Config, addrs)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.extras.certs = map[string]time.Time{}
	for _, c := range certs {
		l.extras.certs[c.Endpoint] = c.NotAfter
	}
}

func (l *Live) refreshCustom(ctx context.Context) {
	if !l.engine.opts.ScanCRDs || !l.engine.caps.Dynamic {
		return
	}
	found := sources.ScanCustomResources(ctx, l.engine.bundle.Discovery,
		l.engine.bundle.Dynamic, l.engine.opts.Namespace, l.engine.opts.CRDTimeout)

	var out []health.CustomResource
	for _, cr := range found {
		out = append(out, health.CustomResource{
			Group: cr.Group, Kind: cr.Kind, Namespace: cr.Namespace,
			Name: cr.Name, Ready: cr.Ready, Reason: cr.Reason,
			Message: cr.Message, Phase: cr.Phase,
		})
	}
	l.mu.Lock()
	l.extras.custom = out
	l.mu.Unlock()
}

// evaluate builds a snapshot from the informer caches, merges the polled
// extras into it, runs the checks and publishes the result.
func (l *Live) evaluate(ctx context.Context) {
	snap, err := l.provider.Snapshot(ctx)
	if err != nil {
		return
	}

	l.mu.RLock()
	ex := l.extras
	l.mu.RUnlock()

	applyExtras(snap, ex)

	selected, err := l.engine.registry.Select(l.engine.opts.Include, l.engine.opts.Exclude)
	if err != nil {
		return
	}
	res := health.Run(ctx, selected, snap)
	res.Findings = filterSeverity(res.Findings, l.engine.opts.MinSeverity)

	l.history.Observe(res)

	l.mu.Lock()
	l.result, l.snapshot = res, snap
	l.mu.Unlock()

	select {
	case l.updates <- struct{}{}:
	default:
	}
}
