package health

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	certsv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// resyncPeriod is the informer's periodic full resync. Watch events drive
// updates; this only guards against a missed event.
const resyncPeriod = 10 * time.Minute

// cacheSyncTimeout bounds the initial list of every informer. A permitted
// resource that still has not synced by then means the cluster is unreachable
// or too slow to serve, and waiting longer would only leave a blank screen.
const cacheSyncTimeout = 60 * time.Second

// watchedResource describes one informer the provider can run: the RBAC
// attributes needed to list and watch it, how to obtain it from the factory,
// and how to copy its cache into a snapshot.
type watchedResource struct {
	name          string
	group         string
	resource      string
	clusterScoped bool
	informer      func(informers.SharedInformerFactory) cache.SharedIndexInformer
	fill          func(*Snapshot, cache.SharedIndexInformer)
}

// watchedResources lists every resource the checks read, mirroring the List
// calls made by ListProvider so both providers feed checks the same data.
var watchedResources = []watchedResource{
	{
		name: "pods", resource: "pods",
		informer: func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().Pods().Informer()
		},
		fill: func(s *Snapshot, i cache.SharedIndexInformer) { s.Pods = storeItems[corev1.Pod](i) },
	},
	{
		name: "deployments", group: "apps", resource: "deployments",
		informer: func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Apps().V1().Deployments().Informer()
		},
		fill: func(s *Snapshot, i cache.SharedIndexInformer) { s.Deployments = storeItems[appsv1.Deployment](i) },
	},
	{
		name: "replicasets", group: "apps", resource: "replicasets",
		informer: func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Apps().V1().ReplicaSets().Informer()
		},
		fill: func(s *Snapshot, i cache.SharedIndexInformer) { s.ReplicaSets = storeItems[appsv1.ReplicaSet](i) },
	},
	{
		name: "statefulsets", group: "apps", resource: "statefulsets",
		informer: func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Apps().V1().StatefulSets().Informer()
		},
		fill: func(s *Snapshot, i cache.SharedIndexInformer) { s.StatefulSets = storeItems[appsv1.StatefulSet](i) },
	},
	{
		name: "daemonsets", group: "apps", resource: "daemonsets",
		informer: func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Apps().V1().DaemonSets().Informer()
		},
		fill: func(s *Snapshot, i cache.SharedIndexInformer) { s.DaemonSets = storeItems[appsv1.DaemonSet](i) },
	},
	{
		name: "jobs", group: "batch", resource: "jobs",
		informer: func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Batch().V1().Jobs().Informer()
		},
		fill: func(s *Snapshot, i cache.SharedIndexInformer) { s.Jobs = storeItems[batchv1.Job](i) },
	},
	{
		name: "cronjobs", group: "batch", resource: "cronjobs",
		informer: func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Batch().V1().CronJobs().Informer()
		},
		fill: func(s *Snapshot, i cache.SharedIndexInformer) { s.CronJobs = storeItems[batchv1.CronJob](i) },
	},
	{
		name: "nodes", resource: "nodes", clusterScoped: true,
		informer: func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().Nodes().Informer()
		},
		fill: func(s *Snapshot, i cache.SharedIndexInformer) { s.Nodes = storeItems[corev1.Node](i) },
	},
	{
		name: "pvcs", resource: "persistentvolumeclaims",
		informer: func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().PersistentVolumeClaims().Informer()
		},
		fill: func(s *Snapshot, i cache.SharedIndexInformer) { s.PVCs = storeItems[corev1.PersistentVolumeClaim](i) },
	},
	{
		name: "services", resource: "services",
		informer: func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().Services().Informer()
		},
		fill: func(s *Snapshot, i cache.SharedIndexInformer) { s.Services = storeItems[corev1.Service](i) },
	},
	{
		name: "endpointslices", group: "discovery.k8s.io", resource: "endpointslices",
		informer: func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Discovery().V1().EndpointSlices().Informer()
		},
		fill: func(s *Snapshot, i cache.SharedIndexInformer) {
			s.EndpointSlices = storeItems[discoveryv1.EndpointSlice](i)
		},
	},
	{
		name: "events", resource: "events",
		informer: func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().Events().Informer()
		},
		fill: func(s *Snapshot, i cache.SharedIndexInformer) { s.Events = storeItems[corev1.Event](i) },
	},
	{
		name: "csrs", group: "certificates.k8s.io", resource: "certificatesigningrequests", clusterScoped: true,
		informer: func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Certificates().V1().CertificateSigningRequests().Informer()
		},
		fill: func(s *Snapshot, i cache.SharedIndexInformer) {
			s.CSRs = storeItems[certsv1.CertificateSigningRequest](i)
		},
	},
}

// activeInformer pairs a running informer with the resource it serves.
type activeInformer struct {
	res watchedResource
	inf cache.SharedIndexInformer
}

// InformerProvider serves snapshots from warm, watch-fed caches. After the
// initial sync a snapshot costs no API calls, which is what makes the dashboard
// a live view rather than a poll loop.
type InformerProvider struct {
	namespace string
	caps      *Capabilities
	active    []activeInformer
	// degraded is the capability notes plus one line per resource the
	// credential may not watch.
	degraded []string

	mu sync.Mutex
	// changed is closed and replaced whenever a watched object moves, so any
	// number of waiters can block on the current channel.
	changed chan struct{}
}

// NewInformerProvider starts an informer for every resource the credential may
// list and watch, and blocks until their caches have synced. Resources RBAC
// denies are skipped and reported as degradation: an informer that cannot list
// never syncs, and waiting on it would hang forever. It errors when no resource
// is readable or the permitted caches fail to sync in time.
func NewInformerProvider(ctx context.Context, kube kubernetes.Interface, namespace string, caps *Capabilities) (*InformerProvider, error) {
	var opts []informers.SharedInformerOption
	if namespace != "" {
		opts = append(opts, informers.WithNamespace(namespace))
	}
	factory := informers.NewSharedInformerFactoryWithOptions(kube, resyncPeriod, opts...)

	p := &InformerProvider{
		namespace: namespace,
		caps:      caps,
		changed:   make(chan struct{}),
	}
	if caps != nil {
		p.degraded = append(p.degraded, caps.Degraded...)
	}

	permitted, denied, err := permittedResources(ctx, kube, namespace)
	if err != nil {
		return nil, err
	}
	for _, r := range denied {
		p.degraded = append(p.degraded, fmt.Sprintf("%s: list/watch denied", r.name))
	}

	handler := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { p.signal() },
		UpdateFunc: func(any, any) { p.signal() },
		DeleteFunc: func(any) { p.signal() },
	}
	// Instantiating each informer registers it with the factory before Start.
	for _, r := range permitted {
		inf := r.informer(factory)
		if _, err := inf.AddEventHandler(handler); err != nil {
			return nil, err
		}
		p.active = append(p.active, activeInformer{res: r, inf: inf})
	}

	factory.Start(ctx.Done())

	syncCtx, cancel := context.WithTimeout(ctx, cacheSyncTimeout)
	defer cancel()
	factory.WaitForCacheSync(syncCtx.Done())

	var unsynced []string
	for _, a := range p.active {
		if !a.inf.HasSynced() {
			unsynced = append(unsynced, a.res.name)
		}
	}
	if len(unsynced) > 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("timed out after %s waiting for cluster state (%s)", cacheSyncTimeout, strings.Join(unsynced, ", "))
	}
	return p, nil
}

// permittedResources splits watchedResources by whether the credential may
// list and watch them in namespace. It errors when no resource is permitted,
// returning the underlying review error when the cluster could not be asked.
func permittedResources(ctx context.Context, kube kubernetes.Interface, namespace string) (permitted, denied []watchedResource, err error) {
	type outcome struct {
		allowed bool
		err     error
	}
	results := make([]outcome, len(watchedResources))

	var wg sync.WaitGroup
	for i, r := range watchedResources {
		wg.Add(1)
		go func(i int, r watchedResource) {
			defer wg.Done()
			ns := namespace
			if r.clusterScoped {
				ns = ""
			}
			allowed := true
			for _, verb := range []string{"list", "watch"} {
				ok, err := accessReview(ctx, kube, authv1.ResourceAttributes{
					Verb: verb, Group: r.group, Resource: r.resource, Namespace: ns,
				})
				if err != nil {
					results[i] = outcome{err: err}
					return
				}
				allowed = allowed && ok
			}
			results[i] = outcome{allowed: allowed}
		}(i, r)
	}
	wg.Wait()

	var errs []error
	for i, r := range watchedResources {
		switch {
		case results[i].err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", r.name, results[i].err))
			denied = append(denied, r)
		case results[i].allowed:
			permitted = append(permitted, r)
		default:
			denied = append(denied, r)
		}
	}
	if len(permitted) == 0 {
		if len(errs) > 0 {
			return nil, nil, unreadableClusterError(errs)
		}
		return nil, nil, fmt.Errorf("could not read cluster state: credential may not list or watch any watched resource")
	}
	return permitted, denied, nil
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
		degraded:        p.degraded,
	}
	if p.caps != nil {
		s.caps = p.caps.Map()
	}

	for _, a := range p.active {
		a.res.fill(s, a.inf)
	}

	// Cache order varies between reads. Sorting keeps the dashboard's node
	// gauges and tables from reshuffling on every refresh.
	sortByName(s.Nodes, func(n corev1.Node) string { return n.Name })
	sortByName(s.Pods, func(p corev1.Pod) string { return p.Namespace + "/" + p.Name })

	s.index()
	return s, nil
}

// sortByName orders a slice by a derived key.
func sortByName[T any](items []T, key func(T) string) {
	sort.Slice(items, func(i, j int) bool { return key(items[i]) < key(items[j]) })
}

// storeItems copies an informer's cached objects into a slice of values, since
// the snapshot owns its copy and checks must not mutate cache objects.
func storeItems[T any](inf cache.SharedIndexInformer) []T {
	objs := inf.GetStore().List()
	out := make([]T, 0, len(objs))
	for _, o := range objs {
		if p, ok := o.(*T); ok && p != nil {
			out = append(out, *p)
		}
	}
	return out
}
