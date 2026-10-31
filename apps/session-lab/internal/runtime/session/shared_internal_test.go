package session

import (
	"context"
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
