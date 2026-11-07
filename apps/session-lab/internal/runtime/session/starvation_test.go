package session_test

import (
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/session"
)

// boundaryRecorder captures the boundary kinds the runner reports, and ignores
// every other metric.
type boundaryRecorder struct {
	kinds []string
}

func (b *boundaryRecorder) SessionArrived()                      {}
func (b *boundaryRecorder) SessionAcquired()                     {}
func (b *boundaryRecorder) SessionSettled(string, model.Outcome) {}
func (b *boundaryRecorder) QueueWait(string, float64)            {}
func (b *boundaryRecorder) Acquire(string, string, float64)      {}
func (b *boundaryRecorder) Hold(string, string, float64)         {}
func (b *boundaryRecorder) Reset(string, float64)                {}
func (b *boundaryRecorder) Release(string, float64)              {}
func (b *boundaryRecorder) Resume(string, float64)               {}
func (b *boundaryRecorder) Resources(string, session.Reading)    {}
func (b *boundaryRecorder) SessionUsage(float64, float64)        {}
func (b *boundaryRecorder) NodeCPU(float64, int)                 {}
func (b *boundaryRecorder) BootStarted()                         {}
func (b *boundaryRecorder) BootCPU(float64)                      {}
func (b *boundaryRecorder) BootFinished(float64)                 {}
func (b *boundaryRecorder) Pool(int32, int32)                    {}
func (b *boundaryRecorder) Template(float64, float64)            {}
func (b *boundaryRecorder) Boundary(kind string)                 { b.kinds = append(b.kinds, kind) }

func hasKind(kinds []string, want string) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

// TestSlotStarvationIsReported is the saturation signal: when the pool has no
// healthy slot and cannot create one, the run has to say so instead of leaving it
// to be inferred from a pile of acquisition failures.
func TestSlotStarvationIsReported(t *testing.T) {
	cfg := config(model.PolicyReuse, 2, 10*time.Millisecond)
	cfg.ContextsPerBrowser = 8
	cfg.Rate = 2

	plan, err := schedule.Generate(cfg, 1)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "starve.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	fake := sandbox.NewFake()
	// Nothing can be created: every acquisition attempt is refused, so the slot
	// pool's provider fails and the session never gets capacity.
	fake.AcquireErrors = []error{
		errors.New("fake: refusing"), errors.New("fake: refusing"), errors.New("fake: refusing"),
		errors.New("fake: refusing"), errors.New("fake: refusing"), errors.New("fake: refusing"),
	}
	rec := &boundaryRecorder{}
	runner := session.New(cfg, session.Options{
		Adapter: fake, Store: st, Plan: plan, Recorder: rec,
		Log: log.New(io.Discard, "", 0),
	})
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !hasKind(rec.kinds, "slot_starvation") {
		t.Fatalf("boundaries = %v, want slot_starvation when no slot can be had", rec.kinds)
	}
}

// TestNoStarvationBoundaryWhenSlotsExist keeps the signal meaningful: a normal run
// must not report starvation.
func TestNoStarvationBoundaryWhenSlotsExist(t *testing.T) {
	cfg := config(model.PolicyReuse, 2, 10*time.Millisecond)
	cfg.ContextsPerBrowser = 8
	cfg.Rate = 2

	plan, _ := schedule.Generate(cfg, 1)
	st, err := store.Open(filepath.Join(t.TempDir(), "nostarve.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	rec := &boundaryRecorder{}
	runner := session.New(cfg, session.Options{
		Adapter: sandbox.NewFake(), Store: st, Plan: plan, Recorder: rec,
		Log: log.New(io.Discard, "", 0),
	})
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if hasKind(rec.kinds, "slot_starvation") {
		t.Fatalf("boundaries = %v, a run with capacity must not report starvation", rec.kinds)
	}
}
