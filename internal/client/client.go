package client

import (
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// New builds a Kubernetes clientset. If kubeconfigPath is empty, it falls back
// to the default kubeconfig discovery order (KUBECONFIG env, ~/.kube/config,
// in-cluster service account).
func New(kubeconfigPath string) (kubernetes.Interface, error) {
	config, err := RestConfig(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

// RestConfig resolves a *rest.Config using the same discovery order as New.
func RestConfig(kubeconfigPath string) (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		loadingRules.ExplicitPath = kubeconfigPath
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
}

// Health checks fan out across many resource types at once, and client-go's
// default 5 QPS makes that crawl. These limits keep a full scan responsive
// while staying well inside the API server's own priority-and-fairness budget.
const (
	healthQPS   = 50
	healthBurst = 100
)

// Bundle holds every client the health engine needs. Metrics is nil when the
// metrics API cannot be reached; callers must treat it as optional.
type Bundle struct {
	Kube      kubernetes.Interface
	Dynamic   dynamic.Interface
	Discovery discovery.DiscoveryInterface
	Metrics   metricsclient.Interface
	Config    *rest.Config
}

// NewBundle builds the full client set for health checks. A failure to build
// the metrics client is not fatal — the health engine degrades instead.
func NewBundle(kubeconfigPath string) (*Bundle, error) {
	config, err := RestConfig(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	config.QPS, config.Burst = healthQPS, healthBurst

	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	b := &Bundle{
		Kube:      kube,
		Dynamic:   dyn,
		Discovery: kube.Discovery(),
		Config:    config,
	}
	if mc, err := metricsclient.NewForConfig(config); err == nil {
		b.Metrics = mc
	}
	return b, nil
}
