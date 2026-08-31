package health

import (
	"context"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// resyncPeriod is the informer's periodic full resync. Watch events drive
// updates; this only guards against a missed event.
const resyncPeriod = 10 * time.Minute

// InformerProvider serves snapshots from warm, watch-fed caches. After the
// initial sync a snapshot costs no API calls, which is what makes the dashboard
// a live view rather than a poll loop.
type InformerProvider struct {
	factory   informers.SharedInformerFactory
	namespace string
	caps      *Capabilities

	mu sync.Mutex
	// changed is closed and replaced whenever a watched object moves, so any
	// number of waiters can block on the current channel.
	changed chan struct{}
}

// NewInformerProvider starts informers for every resource the checks read and
// blocks until their caches have synced.
func NewInformerProvider(ctx context.Context, kube kubernetes.Interface, namespace string, caps *Capabilities) (*InformerProvider, error) {
	var opts []informers.SharedInformerOption
	if namespace != "" {
		opts = append(opts, informers.WithNamespace(namespace))
	}
	factory := informers.NewSharedInformerFactoryWithOptions(kube, resyncPeriod, opts...)

	p := &InformerProvider{
		factory:   factory,
		namespace: namespace,
		caps:      caps,
		changed:   make(chan struct{}),
	}

	// Instantiating each informer registers it with the factory before Start.
	all := []cache.SharedIndexInformer{
		factory.Core().V1().Pods().Informer(),
		factory.Apps().V1().Deployments().Informer(),
		factory.Apps().V1().ReplicaSets().Informer(),
		factory.Apps().V1().StatefulSets().Informer(),
		factory.Apps().V1().DaemonSets().Informer(),
		factory.Batch().V1().Jobs().Informer(),
		factory.Batch().V1().CronJobs().Informer(),
		factory.Core().V1().Nodes().Informer(),
		factory.Core().V1().PersistentVolumeClaims().Informer(),
		factory.Core().V1().Services().Informer(),
		factory.Discovery().V1().EndpointSlices().Informer(),
		factory.Core().V1().Events().Informer(),
	}
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { p.signal() },
		UpdateFunc: func(any, any) { p.signal() },
		DeleteFunc: func(any) { p.signal() },
	}
	for _, inf := range all {
		if _, err := inf.AddEventHandler(handler); err != nil {
			return nil, err
		}
	}

	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())

	return p, nil
}

// signal wakes everything waiting on Changed.
func (p *InformerProvider) signal() {
	p.mu.Lock()
	defer p.mu.Unlock()
	close(p.changed)
	p.changed = make(chan struct{})
}

// Changed returns a channel closed on the next watched-object change. Callers
// re-read it each time, since it is replaced after every signal.
func (p *InformerProvider) Changed() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.changed
}

// Snapshot builds a snapshot from the informer caches without hitting the API.
func (p *InformerProvider) Snapshot(ctx context.Context) (*Snapshot, error) {
	s := &Snapshot{
		Namespace:       p.namespace,
		Taken:           time.Now(),
		VolumeStats:     map[string]VolumeStat{},
		NodeFilesystems: map[string]NodeFsStat{},
		CertExpiry:      map[string]time.Time{},
		caps:            map[Capability]bool{},
	}
	if p.caps != nil {
		s.caps = p.caps.Map()
		s.degraded = p.caps.Degraded
	}

	f := p.factory
	if l, err := f.Core().V1().Pods().Lister().List(labelsEverything()); err == nil {
		s.Pods = derefAll(l)
	}
	if l, err := f.Apps().V1().Deployments().Lister().List(labelsEverything()); err == nil {
		s.Deployments = derefAll(l)
	}
	if l, err := f.Apps().V1().ReplicaSets().Lister().List(labelsEverything()); err == nil {
		s.ReplicaSets = derefAll(l)
	}
	if l, err := f.Apps().V1().StatefulSets().Lister().List(labelsEverything()); err == nil {
		s.StatefulSets = derefAll(l)
	}
	if l, err := f.Apps().V1().DaemonSets().Lister().List(labelsEverything()); err == nil {
		s.DaemonSets = derefAll(l)
	}
	if l, err := f.Batch().V1().Jobs().Lister().List(labelsEverything()); err == nil {
		s.Jobs = derefAll(l)
	}
	if l, err := f.Batch().V1().CronJobs().Lister().List(labelsEverything()); err == nil {
		s.CronJobs = derefAll(l)
	}
	if l, err := f.Core().V1().Nodes().Lister().List(labelsEverything()); err == nil {
		s.Nodes = derefAll(l)
	}
	if l, err := f.Core().V1().PersistentVolumeClaims().Lister().List(labelsEverything()); err == nil {
		s.PVCs = derefAll(l)
	}
	if l, err := f.Core().V1().Services().Lister().List(labelsEverything()); err == nil {
		s.Services = derefAll(l)
	}
	if l, err := f.Discovery().V1().EndpointSlices().Lister().List(labelsEverything()); err == nil {
		s.EndpointSlices = derefAll(l)
	}
	if l, err := f.Core().V1().Events().Lister().List(labelsEverything()); err == nil {
		s.Events = derefAll(l)
	}

	// Listers return cache order, which varies between reads. Sorting keeps the
	// dashboard's node gauges and tables from reshuffling on every refresh.
	sortByName(s.Nodes, func(n corev1.Node) string { return n.Name })
	sortByName(s.Pods, func(p corev1.Pod) string { return p.Namespace + "/" + p.Name })

	s.index()
	return s, nil
}

// sortByName orders a slice by a derived key.
func sortByName[T any](items []T, key func(T) string) {
	sort.Slice(items, func(i, j int) bool { return key(items[i]) < key(items[j]) })
}

// derefAll converts a lister's slice of pointers into a slice of values, since
// the snapshot owns its copy and checks must not mutate cache objects.
func derefAll[T any](in []*T) []T {
	out := make([]T, 0, len(in))
	for _, p := range in {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out
}
