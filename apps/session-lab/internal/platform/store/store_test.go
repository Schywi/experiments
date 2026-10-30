package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "run.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSessionTransitionsSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	session := model.Session{
		ID: 1, Bucket: "30s", Policy: model.PolicyReuse, Namespace: "sandbox-demo",
		Template: "browser-tpl", Pool: "browser-pool", Outcome: model.OutcomePending,
		ArrivalNS: 1000,
	}
	if err := s.AddSession(session); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Each transition is its own committed write, so a process that dies between
	// them still leaves the last one on disk.
	session.ClaimCreatedNS = 2000
	session.ReadyNS = 3000
	session.Outcome = model.OutcomeSuccess
	session.CPUUsec = 4242
	if err := s.UpdateSession(session); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := s.AddEvent(model.Event{SessionID: 1, AtNS: 3000, Kind: "ready"}); err != nil {
		t.Fatalf("event: %v", err)
	}
	// No Close: reopening must work on a database whose writes already committed.
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	rows, err := s2.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 session, got %d", len(rows))
	}
	got := rows[0]
	if got.ReadyNS != 3000 || got.Outcome != model.OutcomeSuccess || got.CPUUsec != 4242 {
		t.Fatalf("row did not survive: %+v", got)
	}
}

func TestOutcomeIdentityHolds(t *testing.T) {
	s := openTemp(t)
	const planned = 100
	// Distribution deliberately includes every terminal outcome plus a session
	// still waiting: the identity counts waiting on its own side.
	distribution := map[model.Outcome]int{
		model.OutcomeSuccess:           60,
		model.OutcomeTimeout:           10,
		model.OutcomeAcquisitionFailed: 12,
		model.OutcomeResetFailed:       6,
		model.OutcomeDestroyFailed:     4,
		model.OutcomeLostUnknown:       3,
		model.OutcomeWaiting:           5,
	}

	id := int64(0)
	for outcome, count := range distribution {
		for i := 0; i < count; i++ {
			id++
			if err := s.AddSession(model.Session{
				ID: id, Bucket: "2m", Policy: model.PolicyDestroy, Namespace: "sandbox-demo",
				Template: "browser-tpl", Pool: "browser-pool", ArrivalNS: id,
				Outcome: model.OutcomePending,
			}); err != nil {
				t.Fatalf("add: %v", err)
			}
			if err := s.UpdateSession(model.Session{ID: id, Outcome: outcome}); err != nil {
				t.Fatalf("update: %v", err)
			}
		}
	}

	rows, err := s.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(rows) != planned {
		t.Fatalf("planned %d sessions, stored %d", planned, len(rows))
	}
	counts := map[model.Outcome]int{}
	for _, row := range rows {
		counts[row.Outcome]++
	}
	failures := 0
	for _, outcome := range model.Failures {
		failures += counts[outcome]
	}
	if total := counts[model.OutcomeSuccess] + failures + counts[model.OutcomeWaiting]; total != planned {
		t.Fatalf("ok(%d) + failures(%d) + waiting(%d) = %d, planned %d",
			counts[model.OutcomeSuccess], failures, counts[model.OutcomeWaiting], total, planned)
	}
	if counts[model.OutcomePending] != 0 {
		t.Fatalf("%d sessions never reached a terminal outcome", counts[model.OutcomePending])
	}
}

func TestRunConfigIsRecorded(t *testing.T) {
	s := openTemp(t)
	cfg := model.RunConfig{
		RunID: "r1", Sessions: 7, Policy: model.PolicySuspend, Namespace: "sandbox-demo",
		Pool: "browser-pool", OutDir: t.TempDir(), SampleSeconds: 10,
	}
	now := time.Unix(1700000000, 0)
	if err := s.StartRun(cfg, now); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s.EndRun("r1", now.Add(time.Minute),
		[]ReplicaSample{{AtNS: 1, Desired: 3, Ready: 2}},
		RunRuntime{ArrivalWindowNS: 600_000_000_000, PeakWaiting: 7, CleanupFailures: 0}); err != nil {
		t.Fatalf("end: %v", err)
	}
	facts, err := s.RunRuntime("r1")
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	if facts.ArrivalWindowNS != 600_000_000_000 || facts.PeakWaiting != 7 {
		t.Fatalf("runtime facts lost: %+v", facts)
	}
	started, ended, err := s.RunWindow("r1")
	if err != nil || started == 0 || ended <= started {
		t.Fatalf("window: started=%d ended=%d err=%v", started, ended, err)
	}
}
