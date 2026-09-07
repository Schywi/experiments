package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"

	api "github.com/lucasmirandoliveira/experiments/apps/controller/api/v1alpha1"
	"github.com/lucasmirandoliveira/experiments/apps/controller/internal/controller"
	"github.com/lucasmirandoliveira/experiments/apps/controller/internal/httpapi"
	"github.com/lucasmirandoliveira/experiments/apps/controller/internal/intent"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	var metricsAddr, probeAddr, listen string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "metrics address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8083", "health probe address")
	flag.StringVar(&listen, "listen-address", ":8081", "replication intent HTTP address")
	flag.Parse()
	namespace, name := os.Getenv("WORM_NAMESPACE"), os.Getenv("WORM_NAME")
	if namespace == "" || name == "" {
		panic("WORM_NAMESPACE and WORM_NAME must be set")
	}
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), managerOptions(scheme, namespace, metricsAddr, probeAddr))
	if err != nil {
		panic(err)
	}
	if err := (&controller.WormReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		panic(err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		panic(err)
	}
	var cacheReady atomic.Bool
	if err := mgr.AddReadyzCheck("cache", readyCheck(&cacheReady)); err != nil {
		panic(err)
	}
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		cacheReady.Store(true)
		defer cacheReady.Store(false)
		return serveIntents(ctx, listen, intent.Service{Client: mgr.GetClient(), Worm: types.NamespacedName{Namespace: namespace, Name: name}})
	})); err != nil {
		panic(err)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		panic(err)
	}
}

func managerOptions(scheme *runtime.Scheme, namespace, metricsAddr, probeAddr string) ctrl.Options {
	return ctrl.Options{
		Scheme:                 scheme,
		Cache:                  cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}},
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
	}
}

func readyCheck(ready *atomic.Bool) healthz.Checker {
	return func(*http.Request) error {
		if ready.Load() {
			return nil
		}
		return errors.New("cache has not synced")
	}
}

func serveIntents(ctx context.Context, listen string, service intent.Service) error {
	server := &http.Server{Addr: listen, Handler: httpapi.Handler(service)}
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve replication intents: %w", err)
	}
	return nil
}
