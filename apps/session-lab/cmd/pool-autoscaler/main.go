// Command pool-autoscaler keeps a SandboxWarmPool sized to its pending demand.
//
// It runs on the host next to session-lab, against the same kubeconfig. Its
// settings are published to a ConfigMap in the sandbox namespace, which is how a
// run records what the autoscaler was configured to do rather than being told.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/adapter"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/autoscale"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "pool-autoscaler: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		namespace   = flag.String("namespace", "sandbox-demo", "namespace holding the pool and the claims")
		pool        = flag.String("pool", "browser-pool", "SandboxWarmPool to size")
		minR        = flag.Int("min", 1, "lower bound on replicas")
		maxR        = flag.Int("max", 40, "upper bound on replicas")
		interval    = flag.Duration("interval", 5*time.Second, "reconcile interval")
		configMap   = flag.String("configmap", "session-lab-autoscaler", "ConfigMap to publish settings to")
		metricsAt   = flag.String("metrics-addr", ":9110", "address to serve /metrics on; empty disables it")
		kubeconfig  = flag.String("kubeconfig", "", "kubeconfig path; empty uses the default config")
		contextName = flag.String("context", "", "kubeconfig context")
	)
	flag.Parse()

	if *minR < 0 || *maxR < *minR {
		return fmt.Errorf("--min %d and --max %d are not a valid range", *minR, *maxR)
	}

	logger := log.New(os.Stdout, "", log.Ltime|log.Lmicroseconds)

	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: *kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: *contextName},
	)
	restConfig, err := loader.ClientConfig()
	if err != nil {
		return fmt.Errorf("kube config: %w", err)
	}
	core, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("clientset: %w", err)
	}
	kube, err := adapter.NewKube(*kubeconfig, *contextName)
	if err != nil {
		return err
	}

	metrics := autoscale.NewMetrics()
	if *metricsAt != "" {
		go func() {
			mux := http.NewServeMux()
			mux.Handle("/metrics", metrics.Handler())
			logger.Printf("metrics: serving /metrics on %s", *metricsAt)
			if err := http.ListenAndServe(*metricsAt, mux); err != nil {
				logger.Printf("metrics: %v", err)
			}
		}()
	}

	scaler := autoscale.New(autoscale.Config{
		Namespace: *namespace, Pool: *pool,
		Min: int32(*minR), Max: int32(*maxR),
		Interval: *interval, ConfigMap: *configMap, Log: logger,
	}, kube, &publisher{core: core, namespace: *namespace}, metrics)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := scaler.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// publisher writes the autoscaler's settings into a ConfigMap, so the cluster
// carries the configuration a run later reports.
type publisher struct {
	core      kubernetes.Interface
	namespace string
}

func (p *publisher) Publish(ctx context.Context, name string, settings autoscale.Settings) error {
	payload, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: p.namespace,
			Labels: map[string]string{"app.kubernetes.io/name": "session-lab-autoscaler"},
		},
		Data: map[string]string{"settings.json": string(payload)},
	}
	_, err = p.core.CoreV1().ConfigMaps(p.namespace).Create(ctx, desired, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	current, err := p.core.CoreV1().ConfigMaps(p.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	current.Data = desired.Data
	current.Labels = desired.Labels
	_, err = p.core.CoreV1().ConfigMaps(p.namespace).Update(ctx, current, metav1.UpdateOptions{})
	return err
}

var _ autoscale.Publisher = (*publisher)(nil)
