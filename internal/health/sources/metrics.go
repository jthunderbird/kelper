// Package sources fetches the optional cluster data the health checks build on:
// metrics-server readings, kubelet stats, and certificate expiry. Each source
// degrades to empty rather than failing, so a check simply has nothing to
// evaluate when its source is unavailable.
package sources

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// FetchMetrics returns pod and node metrics from metrics-server. A nil client
// or any API error yields empty slices and a nil error: metrics are optional.
func FetchMetrics(ctx context.Context, mc metricsclient.Interface, namespace string) ([]metricsv1beta1.PodMetrics, []metricsv1beta1.NodeMetrics) {
	if mc == nil {
		return nil, nil
	}
	var pods []metricsv1beta1.PodMetrics
	if l, err := mc.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{}); err == nil {
		pods = l.Items
	}
	var nodes []metricsv1beta1.NodeMetrics
	if l, err := mc.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{}); err == nil {
		nodes = l.Items
	}
	return pods, nodes
}
