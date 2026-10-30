// Metrics-server readings for a sandbox pod.
//
// This is the second source next to the cgroup files: it is what the cluster
// itself reports through metrics.k8s.io, so a sample can be cross-checked
// against the numbers the platform advertises. It is also the only source
// available when the runner does not run on the node.
package measure

import (
	"context"
	"fmt"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// PodUsage is one pod's reading from the metrics API.
type PodUsage struct {
	CPUMilli int64
	MemBytes int64
}

// UsageSource reads pod usage from the cluster.
type UsageSource interface {
	PodUsage(ctx context.Context, namespace, podName string) (PodUsage, bool, error)
}

var gvrPodMetrics = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}

// MetricsAPI reads metrics.k8s.io, which the cluster serves when metrics-server
// is installed.
type MetricsAPI struct {
	dyn dynamic.Interface
	mu  sync.Mutex
}

// NewMetricsAPI builds a client for the metrics API.
func NewMetricsAPI(kubeconfig, contextName string) (*MetricsAPI, error) {
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: contextName},
	)
	cfg, err := loader.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kube config: %w", err)
	}
	return NewMetricsAPIForConfig(cfg)
}

// NewMetricsAPIForConfig builds a client from an existing rest config.
func NewMetricsAPIForConfig(cfg *rest.Config) (*MetricsAPI, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("metrics client: %w", err)
	}
	return &MetricsAPI{dyn: dyn}, nil
}

// PodUsage implements UsageSource.
func (m *MetricsAPI) PodUsage(ctx context.Context, namespace, podName string) (PodUsage, bool, error) {
	obj, err := m.dyn.Resource(gvrPodMetrics).Namespace(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return PodUsage{}, false, err
	}
	containers, found, err := nestedSlice(obj.Object, "containers")
	if err != nil || !found {
		return PodUsage{}, false, err
	}
	usage := PodUsage{}
	for _, raw := range containers {
		container, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		usage.CPUMilli += quantityMilli(container["usage"], "cpu")
		usage.MemBytes += quantityBytes(container["usage"], "memory")
	}
	return usage, true, nil
}

// nestedSlice is a local helper so this package does not depend on the
// unstructured package for one call.
func nestedSlice(obj map[string]any, field string) ([]any, bool, error) {
	raw, ok := obj[field]
	if !ok {
		return nil, false, nil
	}
	slice, ok := raw.([]any)
	if !ok {
		return nil, false, fmt.Errorf("%s is not a list", field)
	}
	return slice, true, nil
}

// quantityMilli reads a Kubernetes quantity as milli-units. The metrics API
// answers with strings such as "12m" or "3", or with a number for memory.
func quantityMilli(usage any, field string) int64 {
	values, ok := usage.(map[string]any)
	if !ok {
		return 0
	}
	raw, ok := values[field]
	if !ok {
		return 0
	}
	switch value := raw.(type) {
	case string:
		return parseQuantityMilli(value)
	case float64:
		return int64(value * 1000)
	default:
		return 0
	}
}

// quantityBytes reads a Kubernetes quantity as bytes.
func quantityBytes(usage any, field string) int64 {
	values, ok := usage.(map[string]any)
	if !ok {
		return 0
	}
	raw, ok := values[field]
	if !ok {
		return 0
	}
	switch value := raw.(type) {
	case string:
		return parseQuantityBytes(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}

var _ UsageSource = (*MetricsAPI)(nil)

// Answering reports whether the cluster serves metrics.k8s.io for this
// namespace. A run records the answer, so a reader knows whether the platform's
// own numbers were available while the experiment ran.
func (m *MetricsAPI) Answering(ctx context.Context, namespace string) error {
	_, err := m.dyn.Resource(gvrPodMetrics).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("metrics.k8s.io: %w", err)
	}
	return nil
}
