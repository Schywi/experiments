package main

import (
	"net/http"
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestManagerOptionsScopesCacheToWormNamespace(t *testing.T) {
	opts := managerOptions(runtime.NewScheme(), "worm-lab", ":8080", ":8083")
	if len(opts.Cache.DefaultNamespaces) != 1 {
		t.Fatalf("cache namespaces = %#v, want only worm-lab", opts.Cache.DefaultNamespaces)
	}
	if _, ok := opts.Cache.DefaultNamespaces["worm-lab"]; !ok {
		t.Fatalf("cache namespaces = %#v, want worm-lab", opts.Cache.DefaultNamespaces)
	}
}

func TestReadyCheckRequiresCacheSync(t *testing.T) {
	var ready atomic.Bool
	check := readyCheck(&ready)
	if err := check(&http.Request{}); err == nil {
		t.Fatal("ready check succeeded before cache sync")
	}
	ready.Store(true)
	if err := check(&http.Request{}); err != nil {
		t.Fatalf("ready check after cache sync: %v", err)
	}
}
