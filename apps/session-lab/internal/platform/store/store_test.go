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

func TestInstanceBootCPURoundTrips(t *testing.T) {
	s := openTemp(t)
	instance := model.Instance{
		ID: "inst-1", Kind: "claim", Namespace: "sandbox-demo", Node: "fedora",
		PodUID: "uid-1", CreatedNS: 1000, ReadyNS: 2000,
		Sessions: 3, BootCPUPeakCores: 1.94,
		Class:    "normal", ContextsPerBrowser: 1,
	}
	if err := s.SaveInstance(instance); err != nil {
		t.Fatalf("save: %v", err)
	}
	rows, err := s.Instances()
	if err != nil {
		t.Fatalf("instances: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("instances = %d, want 1", len(rows))
	}
	if rows[0].BootCPUPeakCores != 1.94 {
		t.Fatalf("boot cpu peak = %v, want 1.94", rows[0].BootCPUPeakCores)
	}
	if rows[0].Class != "normal" || rows[0].ContextsPerBrowser != 1 {
		t.Fatalf("class/packing = %q/%d, want normal/1", rows[0].Class, rows[0].ContextsPerBrowser)
	}
}

func TestBootPeakCoresFiltersByClassAndPacking(t *testing.T) {
	s := openTemp(t)
	save := func(id, class string, contexts int, peak float64) {
		t.Helper()
		if err := s.SaveInstance(model.Instance{
			ID: id, Kind: "claim", Class: class,
			ContextsPerBrowser: contexts, BootCPUPeakCores: peak,
		}); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	save("a", "normal", 1, 1.2)
	save("b", "normal", 1, 1.9)
	save("c", "bulk", 1, 3.0)   // different class: must not count
	save("d", "normal", 8, 0.7) // different packing: must not count

	got, err := s.BootPeakCores("normal", 1)
	if err != nil {
		t.Fatalf("peak: %v", err)
	}
	if got != 1.9 {
		t.Fatalf("peak = %v, want the worst normal/1 = 1.9", got)
	}

	// A class with no rows is zero, not an error: that is "not measured yet".
	empty, err := s.BootPeakCores("batch", 1)
	if err != nil || empty != 0 {
		t.Fatalf("unmeasured class = %v (err %v), want 0", empty, err)
	}
}

func TestPriorBootPeakCoresScansDirectory(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, peak float64, class string, contexts int) {
		t.Helper()
		st, err := Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		defer st.Close()
		if err := st.SaveInstance(model.Instance{
			ID: name + "-i", Kind: "claim", Class: class,
			ContextsPerBrowser: contexts, BootCPUPeakCores: peak,
		}); err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
	}
	write("run1.db", 1.5, "normal", 1)
	write("run2.db", 1.89, "normal", 1)
	write("run3.db", 4.0, "bulk", 1)      // different class: excluded
	write("current.db", 2.5, "normal", 1) // the current run: skipped

	got, err := PriorBootPeakCores(dir, "current", "normal", 1)
	if err != nil {
		t.Fatalf("prior peak: %v", err)
	}
	if got != 1.89 {
		t.Fatalf("prior peak = %v, want 1.89 (worst of run1/run2, bulk and current excluded)", got)
	}
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
