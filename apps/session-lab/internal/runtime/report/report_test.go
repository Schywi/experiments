package report_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/report"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
)

// The report is what a reader trusts, so its invariants are tested rather than
// eyeballed: every planned session lands in exactly one bucket, and the per-session
// cost block reflects the rows it came from.
func TestReportAccountsForEverySessionAndPerSessionCost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	cfg := model.RunConfig{
		RunID: "r1", Sessions: 3, Policy: model.PolicyReuse, Namespace: "ns", Pool: "p",
		Template: "t", ContextsPerBrowser: 4, CDPTransport: "ws", FixedArrival: true, Rate: 100,
		Mix: []model.Bucket{
			{Label: "30s", Hold: 30 * time.Second, Count: 1},
			{Label: "2m", Hold: 2 * time.Minute, Count: 1},
			{Label: "5m", Hold: 5 * time.Minute, Count: 1},
		},
	}
	if err := st.StartRun(cfg, time.Now()); err != nil {
		t.Fatalf("start: %v", err)
	}
	base := time.Now().UnixNano()
	rows := []model.Session{
		{ID: 1, Bucket: "30s", Policy: model.PolicyReuse, Namespace: "ns", Template: "t", Pool: "p",
			Outcome: model.OutcomeSuccess, ArrivalNS: base, HoldStartNS: base + 1e8, HoldEndNS: base + 3e8,
			TaskDurationUS: 12000, JSHeapBytes: 512 * 1024, CPUUsec: 1_500_000, MemPeakBytes: 300 << 20,
			CPUShared: true, BrowserContext: "ctx-1"},
		{ID: 2, Bucket: "2m", Policy: model.PolicyReuse, Namespace: "ns", Template: "t", Pool: "p",
			Outcome: model.OutcomeSuccess, ArrivalNS: base, HoldStartNS: base + 1e8, HoldEndNS: base + 9e8,
			TaskDurationUS: 25000, JSHeapBytes: 700 * 1024, CPUUsec: 900_000, MemPeakBytes: 320 << 20,
			CPUShared: true, BrowserContext: "ctx-2"},
		{ID: 3, Bucket: "5m", Policy: model.PolicyReuse, Namespace: "ns", Template: "t", Pool: "p",
			Outcome: model.OutcomeAcquisitionFailed, ArrivalNS: base, Error: "acquire: EOF"},
	}
	for _, row := range rows {
		// AddSession writes the identity of the session; the transitions and the
		// measurements arrive through UpdateSession, as they do at runtime.
		if err := st.AddSession(row); err != nil {
			t.Fatalf("add session %d: %v", row.ID, err)
		}
		if err := st.UpdateSession(row); err != nil {
			t.Fatalf("update session %d: %v", row.ID, err)
		}
	}
	if err := st.EndRun("r1", time.Now(), nil, store.RunRuntime{ArrivalWindowNS: 60e9, PeakWaiting: 1}); err != nil {
		t.Fatalf("end: %v", err)
	}

	plan, err := schedule.Generate(cfg, 1)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	report, err := report.Build(cfg, plan, st)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	total := 0
	for _, count := range report.Outcomes {
		total += count
	}
	if total != cfg.Sessions {
		t.Fatalf("outcomes account for %d sessions, want %d: %+v", total, cfg.Sessions, report.Outcomes)
	}
	if report.PerSession.TaskMS.Count != 2 {
		t.Fatalf("per-session task count = %d, want the 2 sessions that ran in a browser",
			report.PerSession.TaskMS.Count)
	}
	if report.PerSession.TaskMS.P50 < 12 || report.PerSession.TaskMS.P50 > 25 {
		t.Fatalf("task p50 = %.1fms, want between the two recorded values", report.PerSession.TaskMS.P50)
	}
	if report.PerSession.HeapMiB.P50 <= 0 {
		t.Fatalf("heap p50 = %.2f MiB, want a value from the rows", report.PerSession.HeapMiB.P50)
	}
	if report.SharedSessions != 2 {
		t.Fatalf("shared sessions = %d, want 2 (the rows marked cpu_shared)", report.SharedSessions)
	}
}
