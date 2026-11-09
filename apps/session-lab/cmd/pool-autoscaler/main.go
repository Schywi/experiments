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

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/kube"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/measure"
	poolscaler "github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/pool"
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
		contexts    = flag.Int("contexts-per-browser", 1, "sessions one replica carries; demand is divided by this")
		minR        = flag.Int("min", 1, "lower bound on replicas")
		maxR        = flag.Int("max", 40, "upper bound on replicas")
		interval    = flag.Duration("interval", 5*time.Second, "reconcile interval")
		configMap   = flag.String("configmap", "session-lab-autoscaler", "ConfigMap to publish settings to")
		metricsAt   = flag.String("metrics-addr", ":9110", "address to serve /metrics on; empty disables it")
		demandSrc   = flag.String("demand-source", "claims", "claims (as before) or published (the runner's own counts)")
		demandCM    = flag.String("demand-configmap", "session-lab-demand", "ConfigMap the runner publishes demand to")
		maxProv     = flag.Int("max-provisioning", 0, "the pool's bound, published so a run records its arm")
		imageTag    = flag.String("image-tag", "", "this autoscaler's image tag, published so a run records its arm")
		memReserve  = flag.Float64("memory-reserve-mb", 0, "hold growth when MemAvailable falls below this; 0 disables")
		memHigh     = flag.Float64("memory-pressure-high", 0, "hold growth when memory PSI full avg10 exceeds this; 0 disables")
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
	kube, err := kube.NewKube(*kubeconfig, *contextName)
	if err != nil {
		return err
	}

	metrics := poolscaler.NewMetrics()
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

	hostCPU := &measure.NodeCPUUtilization{}

	reader := &demandReader{core: core, namespace: *namespace, name: *demandCM}

	scaler := poolscaler.New(poolscaler.Config{
		Namespace: *namespace, Pool: *pool, ContextsPerBrowser: *contexts,
		Min: int32(*minR), Max: int32(*maxR),
		Interval: *interval, ConfigMap: *configMap, Log: logger,
		DemandSource: *demandSrc, DemandReader: reader.Read,
		MemoryReserveMB: *memReserve, MemPressureHigh: *memHigh,
		MaxProvisioning: *maxProv, ImageTag: *imageTag,
		NodeLoad: func() (poolscaler.NodeLoad, error) {
			util, _, err := hostCPU.Utilization()
			if err != nil {
				return poolscaler.NodeLoad{}, err
			}
			pressure, err := measure.ReadHostCPUPressureFull()
			if err != nil {
				return poolscaler.NodeLoad{}, err
			}
			load := poolscaler.NodeLoad{Utilization: util, PressureFull: pressure}
			// Memory is read best-effort: a host without /proc/pressure/memory
			// keeps sizing on CPU, demand and the capacity bounds.
			memory := measure.ReadHostMemory()
			load.MemTotalMB = float64(memory.TotalBytes) / (1 << 20)
			load.MemAvailableMB = float64(memory.AvailableBytes) / (1 << 20)
			if memPressure, err := measure.ReadHostMemoryPressureFull(); err == nil {
				load.MemoryPressureFull = memPressure
			}
			return load, nil
		},
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

func (p *publisher) Publish(ctx context.Context, name string, settings poolscaler.Settings) error {
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

var _ poolscaler.Publisher = (*publisher)(nil)

// demandReader reads the queue depth the runner publishes. A absent ConfigMap is
// not an error: it means the run has not started or does not publish, and the
// scaler falls back to claims and says so in its settings.
type demandReader struct {
	core      kubernetes.Interface
	namespace string
	name      string
}

func (d *demandReader) Read(ctx context.Context) (poolscaler.Demand, bool, error) {
	cm, err := d.core.CoreV1().ConfigMaps(d.namespace).Get(ctx, d.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return poolscaler.Demand{}, false, nil
	}
	if err != nil {
		return poolscaler.Demand{}, false, err
	}
	body, ok := cm.Data["demand.json"]
	if !ok {
		return poolscaler.Demand{}, false, nil
	}
	var demand poolscaler.Demand
	if err := json.Unmarshal([]byte(body), &demand); err != nil {
		return poolscaler.Demand{}, false, fmt.Errorf("%s/demand.json: %w", d.name, err)
	}
	if demand.Live == 0 {
		return poolscaler.Demand{}, false, nil
	}
	return demand, true, nil
}
