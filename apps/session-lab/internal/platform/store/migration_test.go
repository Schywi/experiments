package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
)

// A run written by an older build must still open. The schema grows by ALTER, and
// without that an upgrade would make every existing run record unreadable — which
// is worse than losing a column.
func TestDatabaseFromAnOlderBuildStillOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.db")

	older, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cfg := model.RunConfig{RunID: "old", Sessions: 1, Policy: model.PolicyReuse, Namespace: "ns", Pool: "p", Template: "t"}
	if err := older.StartRun(cfg, time.Now()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := older.AddSession(model.Session{ID: 1, Bucket: "30s", Policy: model.PolicyReuse, Namespace: "ns", Template: "t", Pool: "p", Outcome: model.OutcomePending, ArrivalNS: time.Now().UnixNano()}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := older.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopening runs the migration loop: every column added since must be
	// present, and a duplicate-column error is not a failure.
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	rows, err := reopened.Sessions()
	if err != nil {
		t.Fatalf("sessions after reopen: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != 1 {
		t.Fatalf("rows = %+v, want the single session written before the upgrade", rows)
	}
	// And the new columns are writable on the migrated database.
	row := rows[0]
	row.TaskDurationUS = 1234
	row.JSHeapBytes = 4096
	row.BrowserContext = "ctx-1"
	row.CPUShared = true
	if err := reopened.UpdateSession(row); err != nil {
		t.Fatalf("update after migration: %v", err)
	}
	again, err := reopened.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if again[0].TaskDurationUS != 1234 || again[0].JSHeapBytes != 4096 || !again[0].CPUShared {
		t.Fatalf("per-session cost did not survive the round trip: %+v", again[0])
	}
}

// Reopening twice must not fail on the second migration pass.
func TestMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.db")
	for attempt := 0; attempt < 3; attempt++ {
		s, err := store.Open(path)
		if err != nil {
			t.Fatalf("open attempt %d: %v", attempt, err)
		}
		s.Close()
	}
}

// A session that is written and then updated keeps every transition: this is what
// makes a killed run auditable.
func TestTransitionsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cfg := model.RunConfig{RunID: "r", Sessions: 1, Policy: model.PolicyReuse, Namespace: "ns", Pool: "p", Template: "t"}
	if err := s.StartRun(cfg, time.Now()); err != nil {
		t.Fatalf("start: %v", err)
	}
	now := time.Now().UnixNano()
	session := model.Session{ID: 1, Bucket: "30s", Policy: model.PolicyReuse, Namespace: "ns", Template: "t", Pool: "p", Outcome: model.OutcomePending, ArrivalNS: now}
	if err := s.AddSession(session); err != nil {
		t.Fatalf("insert: %v", err)
	}
	session.ClaimCreatedNS = now + 1
	session.ReadyNS = now + 2
	session.AdmissionWaitNS = 1
	session.HoldStartNS = now + 3
	session.HoldEndNS = now + 4
	session.Outcome = model.OutcomeSuccess
	session.TaskDurationUS = 17000
	if err := s.UpdateSession(session); err != nil {
		t.Fatalf("update: %v", err)
	}
	s.Close()

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	rows, err := reopened.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	got := rows[0]
	if got.Outcome != model.OutcomeSuccess || got.HoldStartNS == 0 || got.HoldEndNS == 0 || got.TaskDurationUS != 17000 {
		t.Fatalf("transitions lost across reopen: %+v", got)
	}
}
