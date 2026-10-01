package health

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	certsv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

// Snapshot is the cluster state every check reads from. It is fetched once per
// run and shared, so a run costs one List per resource type rather than one per
// check.
type Snapshot struct {
	// Namespace is the scope of the snapshot; empty means all namespaces.
	Namespace string
	// Taken is when the snapshot was assembled.
	Taken time.Time

	Pods           []corev1.Pod
	Deployments    []appsv1.Deployment
	ReplicaSets    []appsv1.ReplicaSet
	StatefulSets   []appsv1.StatefulSet
	DaemonSets     []appsv1.DaemonSet
	Jobs           []batchv1.Job
	CronJobs       []batchv1.CronJob
	Nodes          []corev1.Node
	PVCs           []corev1.PersistentVolumeClaim
	Services       []corev1.Service
	EndpointSlices []discoveryv1.EndpointSlice
	Events         []corev1.Event
	CSRs           []certsv1.CertificateSigningRequest

	// PodMetrics and NodeMetrics are populated only when CapMetrics is present.
	PodMetrics  []metricsv1beta1.PodMetrics
	NodeMetrics []metricsv1beta1.NodeMetrics

	// VolumeStats maps "namespace/pvc-name" to kubelet-reported usage. Only
	// populated when CapNodeProxy is present.
	VolumeStats map[string]VolumeStat
	// NodeFilesystems maps a node name to its kubelet-reported disk usage. Only
	// populated when CapNodeProxy is present.
	NodeFilesystems map[string]NodeFsStat
	// CertExpiry maps an endpoint description to its certificate expiry.
	CertExpiry map[string]time.Time

	// Custom holds custom-resource health rows gathered by the dynamic scan.
	Custom []CustomResource

	// eventsByUID indexes events by involved object UID for O(1) correlation.
	eventsByUID map[types.UID][]corev1.Event

	caps     map[Capability]bool
	degraded []string
}

// VolumeStat is kubelet-reported usage for one PersistentVolumeClaim.
type VolumeStat struct {
	Namespace     string
	PVC           string
	Node          string
	UsedBytes     uint64
	CapacityBytes uint64
	InodesUsed    uint64
	InodesFree    uint64
}

// UsedPercent returns volume fill as a percentage, 0 when capacity is unknown.
func (v VolumeStat) UsedPercent() float64 {
	if v.CapacityBytes == 0 {
		return 0
	}
	return float64(v.UsedBytes) / float64(v.CapacityBytes) * 100
}

// NodeFsStat is a node's own filesystem usage as reported by its kubelet.
type NodeFsStat struct {
	Node          string
	UsedBytes     uint64
	CapacityBytes uint64
	InodesUsed    uint64
	InodesFree    uint64
}

// CustomResource is one custom-resource instance reduced to its health signal.
type CustomResource struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
	// Ready is the evaluated readiness: true, false, or unknown (nil).
	Ready *bool
	// Reason and Message come from the failing condition, when there is one.
	Reason  string
	Message string
	// Phase is the fallback signal when no conditions are present.
	Phase string
}

// Has reports whether a capability is available.
func (s *Snapshot) Has(c Capability) bool { return s.caps[c] }

// Missing returns the capabilities from req that are unavailable. The bool is
// true when all are satisfied.
func (s *Snapshot) Missing(req []Capability) ([]string, bool) {
	var missing []string
	for _, c := range req {
		if !s.caps[c] {
			missing = append(missing, string(c))
		}
	}
	return missing, len(missing) == 0
}

// Degraded returns human-readable notes about unavailable data sources.
func (s *Snapshot) Degraded() []string { return s.degraded }

// EventsFor returns events involving the given object UID, newest first.
func (s *Snapshot) EventsFor(uid types.UID) []corev1.Event {
	return s.eventsByUID[uid]
}

// WarningEventsFor returns only Warning-type events for an object, newest first.
func (s *Snapshot) WarningEventsFor(uid types.UID) []corev1.Event {
	var out []corev1.Event
	for _, e := range s.eventsByUID[uid] {
		if e.Type == corev1.EventTypeWarning {
			out = append(out, e)
		}
	}
	return out
}

// NodeByName returns the named node, or nil.
func (s *Snapshot) NodeByName(name string) *corev1.Node {
	for i := range s.Nodes {
		if s.Nodes[i].Name == name {
			return &s.Nodes[i]
		}
	}
	return nil
}

// index builds the event lookup and must be called once the slices are filled.
func (s *Snapshot) index() {
	s.eventsByUID = map[types.UID][]corev1.Event{}
	for _, e := range s.Events {
		uid := e.InvolvedObject.UID
		if uid == "" {
			continue
		}
		s.eventsByUID[uid] = append(s.eventsByUID[uid], e)
	}
	for uid := range s.eventsByUID {
		evs := s.eventsByUID[uid]
		sort.Slice(evs, func(i, j int) bool {
			return eventTime(evs[i]).After(eventTime(evs[j]))
		})
		s.eventsByUID[uid] = evs
	}
}

// eventTime returns the most recent timestamp available on an event, tolerating
// the several fields Kubernetes has used for this over time.
func eventTime(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.FirstTimestamp.Time
}

// SnapshotProvider supplies cluster state to the check runner. The CLI uses a
// list-based provider that fetches and exits; the dashboard uses an
// informer-backed provider serving warm caches. Checks are identical under both.
type SnapshotProvider interface {
	Snapshot(ctx context.Context) (*Snapshot, error)
}

// ListProvider builds snapshots with one-shot concurrent List calls.
type ListProvider struct {
	Kube      kubernetes.Interface
	Namespace string
	// Caps are the detected cluster capabilities.
	Caps *Capabilities
}

// Snapshot fetches all core cluster state concurrently.
func (p *ListProvider) Snapshot(ctx context.Context) (*Snapshot, error) {
	s := &Snapshot{
		Namespace:       p.Namespace,
		Taken:           time.Now(),
		VolumeStats:     map[string]VolumeStat{},
		NodeFilesystems: map[string]NodeFsStat{},
		CertExpiry:      map[string]time.Time{},
		caps:            map[Capability]bool{},
	}
	if p.Caps != nil {
		s.caps = p.Caps.Map()
		s.degraded = p.Caps.Degraded
	}

	ns := p.Namespace
	opts := metav1.ListOptions{}

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		errs  []error
		names []string
	)
	// fetch runs one list call, recording any error without aborting the rest:
	// a partial snapshot is more useful than none, and RBAC often denies a
	// subset of resources.
	fetches := 0
	fetch := func(name string, fn func() error) {
		fetches++
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				names = append(names, name)
				mu.Unlock()
			}
		}()
	}

	fetch("pods", func() error {
		l, err := p.Kube.CoreV1().Pods(ns).List(ctx, opts)
		if err != nil {
			return err
		}
		s.Pods = l.Items
		return nil
	})
	fetch("deployments", func() error {
		l, err := p.Kube.AppsV1().Deployments(ns).List(ctx, opts)
		if err != nil {
			return err
		}
		s.Deployments = l.Items
		return nil
	})
	fetch("replicasets", func() error {
		l, err := p.Kube.AppsV1().ReplicaSets(ns).List(ctx, opts)
		if err != nil {
			return err
		}
		s.ReplicaSets = l.Items
		return nil
	})
	fetch("statefulsets", func() error {
		l, err := p.Kube.AppsV1().StatefulSets(ns).List(ctx, opts)
		if err != nil {
			return err
		}
		s.StatefulSets = l.Items
		return nil
	})
	fetch("daemonsets", func() error {
		l, err := p.Kube.AppsV1().DaemonSets(ns).List(ctx, opts)
		if err != nil {
			return err
		}
		s.DaemonSets = l.Items
		return nil
	})
	fetch("jobs", func() error {
		l, err := p.Kube.BatchV1().Jobs(ns).List(ctx, opts)
		if err != nil {
			return err
		}
		s.Jobs = l.Items
		return nil
	})
	fetch("cronjobs", func() error {
		l, err := p.Kube.BatchV1().CronJobs(ns).List(ctx, opts)
		if err != nil {
			return err
		}
		s.CronJobs = l.Items
		return nil
	})
	fetch("nodes", func() error {
		l, err := p.Kube.CoreV1().Nodes().List(ctx, opts)
		if err != nil {
			return err
		}
		s.Nodes = l.Items
		return nil
	})
	fetch("pvcs", func() error {
		l, err := p.Kube.CoreV1().PersistentVolumeClaims(ns).List(ctx, opts)
		if err != nil {
			return err
		}
		s.PVCs = l.Items
		return nil
	})
	fetch("services", func() error {
		l, err := p.Kube.CoreV1().Services(ns).List(ctx, opts)
		if err != nil {
			return err
		}
		s.Services = l.Items
		return nil
	})
	fetch("endpointslices", func() error {
		l, err := p.Kube.DiscoveryV1().EndpointSlices(ns).List(ctx, opts)
		if err != nil {
			return err
		}
		s.EndpointSlices = l.Items
		return nil
	})
	fetch("events", func() error {
		l, err := p.Kube.CoreV1().Events(ns).List(ctx, opts)
		if err != nil {
			return err
		}
		s.Events = l.Items
		return nil
	})
	fetch("csrs", func() error {
		l, err := p.Kube.CertificatesV1().CertificateSigningRequests().List(ctx, opts)
		if err != nil {
			return err
		}
		s.CSRs = l.Items
		return nil
	})

	wg.Wait()

	// A partial failure is usually RBAC denying a subset of resources, and is
	// reported as degradation. Total failure means the cluster could not be
	// read at all (unreachable, bad credentials); reporting that as an empty,
	// healthy snapshot would pass a CI gate against a dead cluster.
	if len(errs) == fetches {
		return nil, unreadableClusterError(errs)
	}

	s.index()
	// Copy before appending so the shared capability notes are never mutated.
	s.degraded = append([]string(nil), s.degraded...)
	for i, err := range errs {
		// A Forbidden error repeats the user and resource in a long sentence;
		// the resource name alone says the same thing on a one-line summary.
		if apierrors.IsForbidden(err) {
			s.degraded = append(s.degraded, fmt.Sprintf("%s: list denied", names[i]))
			continue
		}
		s.degraded = append(s.degraded, err.Error())
	}
	return s, nil
}

// unreadableClusterError reports a total read failure. Every resource usually
// fails for the same reason (connection refused, expired credentials), so the
// first error stands for the rest rather than repeating it per resource.
func unreadableClusterError(errs []error) error {
	if len(errs) == 1 {
		return fmt.Errorf("could not read cluster state: %w", errs[0])
	}
	return fmt.Errorf("could not read cluster state: %w (and %d more failures)", errs[0], len(errs)-1)
}

// NewSnapshotForTest builds a snapshot from hand-made fixtures, applying the
// mutators and then computing the internal indexes. All capabilities are
// enabled so capability gating does not interfere with check unit tests.
func NewSnapshotForTest(mutators ...func(*Snapshot)) *Snapshot {
	s := &Snapshot{
		Taken:           time.Now(),
		VolumeStats:     map[string]VolumeStat{},
		NodeFilesystems: map[string]NodeFsStat{},
		CertExpiry:      map[string]time.Time{},
		caps: map[Capability]bool{
			CapMetrics: true, CapNodeProxy: true, CapDynamic: true,
		},
	}
	for _, m := range mutators {
		m(s)
	}
	s.index()
	return s
}

// NewSnapshotForTestWithCaps builds a snapshot with an explicit capability set,
// for exercising the degraded paths that a fully privileged cluster cannot.
func NewSnapshotForTestWithCaps(caps map[Capability]bool) *Snapshot {
	s := NewSnapshotForTest()
	s.caps = caps
	return s
}
