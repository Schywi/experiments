package session

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// TestProbeAddressUsesTheTransportPort is the run B lesson in one assertion: the
// readiness probe covers 9222, and a ws run dials 9224. Probing the wrong port
// declares a dead forwarder healthy.
func TestProbeAddressUsesTheTransportPort(t *testing.T) {
	instance := &sandbox.Instance{ID: "inst-1", PodIP: "10.42.0.9"}

	httpRun := &Runner{cfg: model.RunConfig{}}
	if got := httpRun.probeAddress(instance); got != "10.42.0.9:9222" {
		t.Fatalf("http probe address = %q, want the bridge port 9222", got)
	}

	wsRun := &Runner{cfg: model.RunConfig{CDPTransport: "ws"}}
	if got := wsRun.probeAddress(instance); got != "10.42.0.9:9224" {
		t.Fatalf("ws probe address = %q, want the forwarder port 9224", got)
	}

	// An explicit port wins, so a run can probe a sidecar on a pinned port.
	pinned := &Runner{cfg: model.RunConfig{CDPTransport: "ws", CDPPort: 9333}}
	if got := pinned.probeAddress(instance); got != "10.42.0.9:9333" {
		t.Fatalf("pinned probe address = %q, want 10.42.0.9:9333", got)
	}
}

// TestProbeCDPAcceptsOnlySuccess: /json/version answering 200 means the browser
// can serve; anything else means it cannot.
func TestProbeCDPAcceptsOnlySuccess(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			t.Errorf("probe asked for %s, want /json/version", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Browser":"Chrome/130"}`))
	}))
	defer ok.Close()
	if err := probeCDP(context.Background(), strings.TrimPrefix(ok.URL, "http://"), ok.Client()); err != nil {
		t.Fatalf("healthy probe returned %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusBadGateway)
	}))
	defer bad.Close()
	if err := probeCDP(context.Background(), strings.TrimPrefix(bad.URL, "http://"), bad.Client()); err == nil {
		t.Fatal("a 502 probe reported healthy")
	}
}

// TestProbeCDPFailsOnRefused is the exact failure run B produced on port 9224.
func TestProbeCDPFailsOnRefused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	listener.Close()

	probe := probeCDP(context.Background(), address, &http.Client{})
	if probe == nil {
		t.Fatal("a refused probe reported healthy")
	}
	if !strings.Contains(strings.ToLower(probe.Error()), "refused") {
		t.Fatalf("probe error = %v, want a connection refused", probe)
	}
}

// TestProbeInstanceRejectsAPodWithoutIP keeps the pool from treating an unbound
// sandbox as a browser that answered.
func TestProbeInstanceRejectsAPodWithoutIP(t *testing.T) {
	r := &Runner{cfg: model.RunConfig{}}
	if err := r.probeInstance(context.Background(), &sandbox.Instance{ID: "inst-1"}); err == nil {
		t.Fatal("a pod with no IP probed healthy")
	}
	if err := r.probeInstance(context.Background(), nil); err == nil {
		t.Fatal("a nil instance probed healthy")
	}
}

// TestProbeTimeoutIsShort stops a hanging browser from spending a session's
// deadline: the probe has its own 300 ms budget.
func TestProbeTimeoutIsShort(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer slow.Close()

	start := time.Now()
	if err := probeCDP(context.Background(), strings.TrimPrefix(slow.URL, "http://"), slow.Client()); err == nil {
		t.Fatal("a hanging probe reported healthy")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("probe took %s, want it cut off by the %s timeout", elapsed, probeTimeout)
	}
}
