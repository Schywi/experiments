package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/admission"
)

// The cgroup counter belongs to the pod, so a per-session row carries its share of
// both CPU and memory. Memory was missed, which made 30 sessions of 339 MiB each
// read as ~10 GiB of browser footprint.
func TestDivideReadingSharesCpuAndMemory(t *testing.T) {
	reading := Reading{CPUUsec: 4000, MemCurrent: 400 << 20, MemWorkingSet: 300 << 20}
	divided := divideReading(reading, 4)
	if divided.CPUUsec != 1000 {
		t.Fatalf("cpu = %d, want a quarter of the pod's", divided.CPUUsec)
	}
	if divided.MemCurrent != 100<<20 || divided.MemWorkingSet != 75<<20 {
		t.Fatalf("memory = %d/%d, want a quarter of the pod's 400/300 MiB",
			divided.MemCurrent, divided.MemWorkingSet)
	}

	// An instance serving one session is not divided at all.
	alone := divideReading(reading, 1)
	if alone.MemCurrent != reading.MemCurrent || alone.CPUUsec != reading.CPUUsec {
		t.Fatalf("a single-session browser was divided anyway: %+v", alone)
	}
}

// A list of pages is cycled one per session. The runner has no idea what those
// pages are; it just must not give two sessions the same one by accident.
func TestTargetURLsCyclePerSession(t *testing.T) {
	r := &Runner{cfg: model.RunConfig{TargetURLs: []string{"a", "b", "c"}}}
	want := map[int64]string{1: "a", 2: "b", 3: "c", 4: "a", 5: "b", 30: "c"}
	for sessionID, expected := range want {
		if got := r.targetURL(sessionID); got != expected {
			t.Fatalf("session %d got %q, want %q", sessionID, got, expected)
		}
	}

	// No list means the single URL, and no URL means a blank page.
	single := &Runner{cfg: model.RunConfig{TargetURL: "https://example.com/"}}
	if got := single.targetURL(7); got != "https://example.com/" {
		t.Fatalf("single url = %q", got)
	}
	empty := &Runner{}
	if got := empty.targetURL(1); got != "about:blank" {
		t.Fatalf("default = %q, want about:blank", got)
	}
}

// A cold Chromium spikes to hundreds of millicores. Boot permits exist so those
// spikes do not land together; two sessions booting with a limit of one must
// serialise.
func TestBootConcurrencySerialisesStartups(t *testing.T) {
	gate := admission.NewGate(1)
	ctx := context.Background()

	if err := gate.Acquire(ctx, model.ClassNormal); err != nil {
		t.Fatalf("first permit: %v", err)
	}
	if got := gate.InUse(); got != 1 {
		t.Fatalf("in use = %d after one permit", got)
	}

	blocked := make(chan struct{})
	go func() {
		gate.Acquire(ctx, model.ClassNormal)
		close(blocked)
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case <-blocked:
		t.Fatalf("a second boot was admitted while the permit was held")
	default:
	}

	gate.Release()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatalf("releasing the permit did not admit the waiting boot")
	}
}

// A dead browser is not a reset problem. Eight sessions died with "reset_failed"
// when an OOM-killed pod took its DevTools socket with it, which hid the cause.
func TestBrowserLossIsClassifiedAsLost(t *testing.T) {
	lost := []string{
		"cdp: read: failed to get reader: failed to read frame header: EOF",
		"write: connection reset by peer",
		"use of closed network connection",
	}
	for _, message := range lost {
		if !browserLost(errors.New(message)) {
			t.Fatalf("%q was not classified as a lost browser", message)
		}
	}
	// A real context problem is still a context problem.
	if browserLost(errors.New("Failed to find context with id ctx-1")) {
		t.Fatalf("a missing context was classified as a lost browser")
	}
	if browserLost(nil) {
		t.Fatalf("nil error classified as a lost browser")
	}
}

// The run must be told what the node can host before it starts, not after ten
// minutes of watching sessions queue. The two numbers that bind are physical
// memory against the template's limit, and the pod cap minus the node's own
// workload; CPU is not one of them, because this template carries no CPU limit.
func TestCapacityCeilingUsesMemoryAndPodCap(t *testing.T) {
	const (
		mib          = 1 << 20
		sixteenGiB   = 16 << 30
		eightFortyMi = 840 * mib
	)
	cases := []struct {
		name       string
		mem        int64
		pods       int64
		limit      int64
		offered    float64
		contexts   int
		wantMem    int
		wantPods   int
		wantCeil   int
		wantNeeded int
	}{
		{
			name: "memory binds", mem: sixteenGiB, pods: 110, limit: eightFortyMi,
			offered: 3000, contexts: 8,
			wantMem: 19, wantPods: 98, wantCeil: 19, wantNeeded: 375,
		},
		{
			name: "pod cap binds", mem: sixteenGiB, pods: 20, limit: eightFortyMi,
			offered: 160, contexts: 8,
			wantMem: 19, wantPods: 8, wantCeil: 8, wantNeeded: 20,
		},
		{
			name: "no limit means no memory ceiling", mem: sixteenGiB, pods: 110, limit: 0,
			offered: 80, contexts: 8,
			wantMem: 0, wantPods: 98, wantCeil: 98, wantNeeded: 10,
		},
		{
			name: "a pod cap smaller than the system reservation is zero, not negative",
			mem:  sixteenGiB, pods: 6, limit: eightFortyMi,
			offered: 0, contexts: 0,
			wantMem: 19, wantPods: 0, wantCeil: 19, wantNeeded: 0,
		},
	}
	for _, c := range cases {
		got := capacityCeiling(c.mem, c.pods, c.limit, c.offered, c.contexts)
		if got.BrowsersByMemory != c.wantMem || got.BrowsersByPods != c.wantPods ||
			got.Browsers != c.wantCeil || got.NeededBrowsers != c.wantNeeded {
			t.Fatalf("%s: got memory=%d pods=%d ceiling=%d needed=%d, want %d/%d/%d/%d",
				c.name, got.BrowsersByMemory, got.BrowsersByPods, got.Browsers, got.NeededBrowsers,
				c.wantMem, c.wantPods, c.wantCeil, c.wantNeeded)
		}
	}
}
