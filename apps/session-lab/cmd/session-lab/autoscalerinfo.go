package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/report"
)

// autoscalerInfo asks the cluster what scales this pool, instead of being told.
//
// Three facts, each read at run time: how many HorizontalPodAutoscalers exist in
// the namespace, whether the KEDA CRD is installed, and whether this repository's
// pool autoscaler is alive — which it proves by a heartbeat in its ConfigMap.
func autoscalerInfo(kubeconfig, contextName, namespace, configMap string) (func(context.Context) (string, error), error) {
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: contextName},
	)
	restConfig, err := loader.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kube config: %w", err)
	}
	core, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("clientset: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}

	return func(ctx context.Context) (string, error) {
		state := report.AutoscalerState{}

		hpas, err := core.AutoscalingV2().HorizontalPodAutoscalers(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", fmt.Errorf("list hpa: %w", err)
		}
		state.HPACount = len(hpas.Items)
		_ = autoscalingv2.HorizontalPodAutoscaler{}

		kedaGVR := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
		if _, err := dyn.Resource(kedaGVR).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: 1}); err == nil {
			state.KEDA = true
		}

		cm, err := core.CoreV1().ConfigMaps(namespace).Get(ctx, configMap, metav1.GetOptions{})
		if err == nil {
			present := true
			state.PoolScaler = &present
			var settings struct {
				Min       int32   `json:"min"`
				Max       int32   `json:"max"`
				IntervalS float64 `json:"interval_s"`
				UpdatedNS int64   `json:"updated_ns"`
			}
			if raw, ok := cm.Data["settings.json"]; ok {
				if json.Unmarshal([]byte(raw), &settings) == nil {
					state.Min = settings.Min
					state.Max = settings.Max
					state.IntervalS = settings.IntervalS
					if settings.UpdatedNS > 0 {
						age := time.Since(time.Unix(0, settings.UpdatedNS)).Seconds()
						state.HeartbeatS = age
						state.Stale = age > 30
					}
				}
			}
		}

		payload, err := json.Marshal(state)
		if err != nil {
			return "", err
		}
		return string(payload), nil
	}, nil
}
