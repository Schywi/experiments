package report

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
)

// TestSwapPeakAggregatesPerSession covers the one thing the swap A/B reads that
// had no aggregate before: the per-session memory.swap.current peak. A run with
// no swap available (every peak zero) must report zero rather than a bogus
// minimum, so the zero entry is deliberately part of the sample.
func TestSwapPeakAggregatesPerSession(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "run.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	cfg := model.RunConfig{
		RunID: "swap-test", Namespace: "sandbox-demo", Pool: "browser-pool",
		Policy: model.PolicyDestroy,
	}
	if err := st.StartRun(cfg, time.Now()); err != nil {
		t.Fatalf("start run: %v", err)
	}

	peaks := []int64{0, 64 << 20, 96 << 20}
	plan := schedule.Plan{Rate: 0.05}
	for i, peak := range peaks {
		id := int64(i + 1)
		session := model.Session{
			ID: id, Bucket: "30s", Policy: model.PolicyDestroy,
			Namespace: cfg.Namespace, Template: "browser-tpl", Pool: cfg.Pool,
			Outcome: model.OutcomeSuccess,
			// Timestamps are what make the row countable as a session.
			ArrivalNS: 1, ClaimCreatedNS: 2, ReadyNS: 3, HoldStartNS: 4, HoldEndNS: 5,
			MemPeakBytes: 200 << 20, SwapPeakBytes: peak, Samples: 3,
		}
		if err := st.AddSession(session); err != nil {
			t.Fatalf("add session %d: %v", id, err)
		}
		if err := st.UpdateSession(session); err != nil {
			t.Fatalf("update session %d: %v", id, err)
		}
		plan.Sessions = append(plan.Sessions, schedule.Planned{ID: id, Bucket: "30s"})
	}

	rep, err := Build(cfg, plan, st)
	if err != nil {
		t.Fatalf("build report: %v", err)
	}
	if rep.Resources.SwapPeakMaxMiB != 96 {
		t.Fatalf("swap peak max = %v MiB, want 96", rep.Resources.SwapPeakMaxMiB)
	}
	// percentile() is nearest-rank over the sorted samples: [0 64 96] → index 1.
	if rep.Resources.SwapPeakP50MiB != 64 {
		t.Fatalf("swap peak p50 = %v MiB, want 64", rep.Resources.SwapPeakP50MiB)
	}
}
