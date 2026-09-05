package main

import (
	"flag"
	"net/http"
	"os"

	api "github.com/lucasmirandoliveira/experiments/apps/controller/api/v1alpha1"
	"github.com/lucasmirandoliveira/experiments/apps/controller/internal/controller"
	"github.com/lucasmirandoliveira/experiments/apps/controller/internal/httpapi"
	"github.com/lucasmirandoliveira/experiments/apps/controller/internal/intent"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	var metricsAddr, probeAddr, listen string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "metrics address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "health probe address")
	flag.StringVar(&listen, "listen-address", ":8082", "replication intent HTTP address")
	flag.Parse()
	namespace, name := os.Getenv("WORM_NAMESPACE"), os.Getenv("WORM_NAME")
	if namespace == "" || name == "" {
		panic("WORM_NAMESPACE and WORM_NAME must be set")
	}
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: metricsAddr}, HealthProbeBindAddress: probeAddr})
	if err != nil {
		panic(err)
	}
	if err := (&controller.WormReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		panic(err)
	}
	go func() {
		if err := http.ListenAndServe(listen, httpapi.Handler(intent.Service{Client: mgr.GetClient(), Worm: types.NamespacedName{Namespace: namespace, Name: name}})); err != nil {
			panic(err)
		}
	}()
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		panic(err)
	}
}
