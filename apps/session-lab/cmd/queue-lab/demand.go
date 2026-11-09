package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/session"
)

// configMapDemand publishes the runner's queue depth once a second to a ConfigMap
// in the pool's namespace, where the autoscaler reads it. It is the channel
// research/session-lifecycle-elasticity/27 argues for: the process that holds the
// waiters is the only one that can count them, and the API server is a path the
// autoscaler already talks.
type configMapDemand struct {
	core      kubernetes.Interface
	namespace string
	name      string
	runID     string
	warned    bool
}

// newDemandSink builds the publisher from "namespace/name".
func newDemandSink(kubeconfig, target, runID string) (*configMapDemand, error) {
	namespace, name, ok := strings.Cut(target, "/")
	if !ok || namespace == "" || name == "" {
		return nil, fmt.Errorf("--publish-demand %q is namespace/name", target)
	}
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{},
	)
	restConfig, err := loader.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kube config: %w", err)
	}
	core, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("clientset: %w", err)
	}
	return &configMapDemand{core: core, namespace: namespace, name: name, runID: runID}, nil
}

// PublishDemand implements session.DemandSink.
func (c *configMapDemand) PublishDemand(ctx context.Context, demand session.Demand) error {
	payload, err := json.Marshal(demand)
	if err != nil {
		return err
	}
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.name,
			Namespace: c.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "queue-lab",
				"app.kubernetes.io/managed-by": "queue-lab",
			},
		},
		Data: map[string]string{"demand.json": string(payload)},
	}
	if _, err := c.core.CoreV1().ConfigMaps(c.namespace).Create(ctx, desired, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}
	current, err := c.core.CoreV1().ConfigMaps(c.namespace).Get(ctx, c.name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	current.Data = desired.Data
	current.Labels = desired.Labels
	_, err = c.core.CoreV1().ConfigMaps(c.namespace).Update(ctx, current, metav1.UpdateOptions{})
	return err
}

var _ session.DemandSink = (*configMapDemand)(nil)
