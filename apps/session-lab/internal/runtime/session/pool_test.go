package session

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/slots"
)

func TestPoolForPrecedence(t *testing.T) {
	r := &Runner{cfg: model.RunConfig{
		Pool: "cpu-pool",
		PoolByClass: map[string]string{
			"normal": "gpu-pool",
		},
	}}

	cases := []struct {
		name    string
		planned schedule.Planned
		want    string
	}{
		{"class mapping wins over default", schedule.Planned{}, "gpu-pool"},
		{"per-session pool overrides class", schedule.Planned{Pool: "special-pool"}, "special-pool"},
	}
	for _, c := range cases {
		if got := r.poolFor(c.planned); got != c.want {
			t.Errorf("%s: poolFor = %q, want %q", c.name, got, c.want)
		}
	}

	// A planned session carries no class of its own: the runner's class is the
	// run-wide value, so the unmapped-class fallthrough needs a bulk runner.
	bulk := &Runner{cfg: model.RunConfig{
		Pool: "cpu-pool", Class: model.ClassBulk,
		PoolByClass: map[string]string{"normal": "gpu-pool"},
	}}
	if got := bulk.poolFor(schedule.Planned{}); got != "cpu-pool" {
		t.Errorf("bulk class = %q, want cpu-pool", got)
	}
	if got := bulk.poolFor(schedule.Planned{Pool: "bulk-pool"}); got != "bulk-pool" {
		t.Errorf("per-session bulk override = %q, want bulk-pool", got)
	}
}

func TestPoolNamesDeduplicates(t *testing.T) {
	r := &Runner{cfg: model.RunConfig{
		Pool: "cpu-pool",
		PoolByClass: map[string]string{
			"normal": "gpu-pool",
			"batch":  "cpu-pool", // same as default: must not repeat
			"bulk":   "cpu-pool",
		},
	}}
	got := r.poolNames()
	if len(got) != 2 {
		t.Fatalf("poolNames = %v, want 2 unique pools", got)
	}
	if got[0] != "cpu-pool" || got[1] != "gpu-pool" {
		t.Fatalf("poolNames order = %v, want [cpu-pool gpu-pool]", got)
	}
}

func TestSpoolForOnePoolPerName(t *testing.T) {
	r := &Runner{cfg: model.RunConfig{ContextsPerBrowser: 8}, spools: map[string]*slots.Pool{}}

	first := r.spoolFor("gpu-pool")
	second := r.spoolFor("gpu-pool")
	if first != second {
		t.Fatalf("spoolFor returned different pools for the same name")
	}
	other := r.spoolFor("cpu-pool")
	if first == other {
		t.Fatalf("spoolFor returned the same pool for different names")
	}
	if len(r.spools) != 2 {
		t.Fatalf("spools = %d, want 2", len(r.spools))
	}
}

func TestRetireEverywhereIsNilSafe(t *testing.T) {
	r := &Runner{cfg: model.RunConfig{ContextsPerBrowser: 1}}
	r.retireEverywhere(&sandbox.Instance{ID: "gone"}) // no spools: must not panic
}

func TestDeriveBootConcurrency(t *testing.T) {
	cases := []struct {
		cores, peak float64
		want        int
	}{
		{6, 1.9, 2}, // (6*0.8)/1.9 = 2.52
		{6, 1.5, 3}, // 4.8/1.5 = 3.2
		{6, 1.74, 2},
		{6, 0.5, 9}, // 4.8/0.5 = 9.6
		{6, 10, 1},  // clamps to one
		{0, 1.9, 0}, // no cores: nothing to derive
		{6, 0, 0},   // no peak: nothing to derive
	}
	for _, c := range cases {
		if got := deriveBootConcurrency(c.cores, c.peak); got != c.want {
			t.Errorf("deriveBootConcurrency(%v, %v) = %d, want %d", c.cores, c.peak, got, c.want)
		}
	}
}

// runForRouting opens a store and runs a small plan against a fake, so the pool
// name each claim was created against can be inspected.
func runForRouting(t *testing.T, cfg model.RunConfig) (*sandbox.Fake, *store.Store) {
	t.Helper()
	plan, err := schedule.Generate(cfg, 1)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "run.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	fake := sandbox.NewFake()
	runner := New(cfg, Options{Adapter: fake, Store: st, Plan: plan})
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	return fake, st
}

func TestIsolatePathRoutesClaimsByClass(t *testing.T) {
	cfg := model.RunConfig{
		RunID: "route", Sessions: 4, Policy: model.PolicyDestroy,
		Namespace: "sandbox-demo", Pool: "cpu-pool", Template: "cpu-tpl",
		FixedArrival: true, Rate: 500, SampleSeconds: 1, OutDir: "out",
		PoolByClass: map[string]string{"normal": "gpu-pool"},
		Mix:         []model.Bucket{{Label: "5ms", Hold: 5 * time.Millisecond, Count: 4}},
	}
	fake, st := runForRouting(t, cfg)

	if len(fake.Pools) == 0 {
		t.Fatalf("no claims were created")
	}
	for i, pool := range fake.Pools {
		if pool != "gpu-pool" {
			t.Fatalf("claim %d went to %q, want gpu-pool", i, pool)
		}
	}
	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	for _, row := range rows {
		if row.Pool != "gpu-pool" {
			t.Fatalf("session %d recorded pool %q, want gpu-pool", row.ID, row.Pool)
		}
	}
}

func TestSharedPathRoutesClaimsByClass(t *testing.T) {
	cfg := model.RunConfig{
		RunID: "route-shared", Sessions: 12, Policy: model.PolicyReuse,
		Namespace: "sandbox-demo", Pool: "cpu-pool", Template: "cpu-tpl",
		ContextsPerBrowser: 8, FixedArrival: true, Rate: 500, SampleSeconds: 1, OutDir: "out",
		PoolByClass: map[string]string{"normal": "gpu-pool"},
		Mix:         []model.Bucket{{Label: "5ms", Hold: 5 * time.Millisecond, Count: 12}},
	}
	fake, st := runForRouting(t, cfg)

	if len(fake.Pools) == 0 {
		t.Fatalf("no claims were created")
	}
	for i, pool := range fake.Pools {
		if pool != "gpu-pool" {
			t.Fatalf("claim %d went to %q, want gpu-pool", i, pool)
		}
	}
	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	for _, row := range rows {
		if row.Pool != "gpu-pool" {
			t.Fatalf("session %d recorded pool %q, want gpu-pool", row.ID, row.Pool)
		}
	}
}

// TestSpoolForIsSafeUnderConcurrentSessions is the test the race detector needs to
// report the defect it caught: spoolFor is called from one goroutine per session,
// and it read and wrote the spool map with no lock. A Go map with a concurrent read
// and write does not merely race; it can fault the process, which is a run lost to
// a scheduling accident.
func TestSpoolForIsSafeUnderConcurrentSessions(t *testing.T) {
	r := &Runner{
		cfg:    model.RunConfig{ContextsPerBrowser: 8},
		spools: map[string]*slots.Pool{},
	}
	pools := []string{"cpu-pool", "gpu-pool", "bulk-pool"}
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(pool string) {
			defer wg.Done()
			if got := r.spoolFor(pool); got == nil {
				t.Errorf("spoolFor(%q) returned nil", pool)
			}
			// The other two readers of the same map, on the same goroutine.
			r.retireEverywhere(&sandbox.Instance{ID: "nobody"})
			r.poolShape()
		}(pools[i%len(pools)])
	}
	wg.Wait()
	r.spoolsMu.Lock()
	defer r.spoolsMu.Unlock()
	if len(r.spools) != len(pools) {
		t.Fatalf("%d spools for %d pools: one creation was lost", len(r.spools), len(pools))
	}
}
