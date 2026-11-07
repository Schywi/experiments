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

// deadFirstBrowser fails the browser-context call for the first instance it is
// asked about, and behaves afterwards. It models run B: one browser that never
// answered on the CDP port, while every other browser was fine.
type deadFirstBrowser struct {
	*sandbox.Fake
	dead          string
	failOpen      bool
	failClose     bool
	openCalls     int
	closeCalls    int
	failedOn      string
	closeFailedOn string
}

func (a *deadFirstBrowser) OpenSession(ctx context.Context, instance *sandbox.Instance, url string) (sandbox.BrowserSession, error) {
	a.openCalls++
	if a.dead == "" {
		a.dead = instance.ID
	}
	if a.failOpen && instance.ID == a.dead {
		a.failedOn = instance.ID
		return sandbox.BrowserSession{}, errors.New(
			"discover http://10.42.0.238:9224: dial tcp 10.42.0.238:9224: connect: connection refused")
	}
	return a.Fake.OpenSession(ctx, instance, url)
}

func (a *deadFirstBrowser) CloseSession(ctx context.Context, instance *sandbox.Instance, s sandbox.BrowserSession) error {
	a.closeCalls++
	if a.failClose && instance.ID == a.dead {
		a.closeFailedOn = instance.ID
		return errors.New("close context: cdp: dial tcp 10.42.0.238:9224: connect: connection refused")
	}
	return a.Fake.CloseSession(ctx, instance, s)
}

func runWith(t *testing.T, cfg model.RunConfig, adapter sandbox.Adapter) *store.Store {
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
	runner := session.New(cfg, session.Options{
		Adapter: adapter, Store: st, Plan: plan, Log: log.New(io.Discard, "", 0),
	})
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	return st
}

// TestIsolateFailureRetiresTheInstance is the bug from run B in miniature: the
// browser that fails at the CDP dial must leave the pool, so the next session
// lands on a different instance instead of failing the same way 1378 times.
func TestIsolateFailureRetiresTheInstance(t *testing.T) {
	cfg := config(model.PolicyReuse, 2, 10*time.Millisecond)
	cfg.ContextsPerBrowser = 8
	cfg.Rate = 2 // sequential: the second session must start after the first one finished
	adapter := &deadFirstBrowser{Fake: sandbox.NewFake(), failOpen: true}

	st := runWith(t, cfg, adapter)
	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	if rows[0].Outcome != model.OutcomeResetFailed {
		t.Fatalf("session 1 ended %q, want reset_failed at the dead browser", rows[0].Outcome)
	}
	if rows[1].Outcome != model.OutcomeSuccess {
		t.Fatalf("session 2 ended %q (%s), want success on a second instance",
			rows[1].Outcome, rows[1].Error)
	}
	if rows[0].Instance == rows[1].Instance {
		t.Fatalf("both sessions used %s: the failed instance was not retired", rows[0].Instance)
	}
	if adapter.failedOn == "" {
		t.Fatal("the fault never fired: the test is not exercising the isolate path")
	}
}

// TestCloseFailureRetiresTheInstance covers the second failure path: the hold
// worked, the browser died before the context could be closed.
func TestCloseFailureRetiresTheInstance(t *testing.T) {
	cfg := config(model.PolicyReuse, 2, 10*time.Millisecond)
	cfg.ContextsPerBrowser = 8
	cfg.Rate = 2 // sequential: the second session must start after the first one finished
	adapter := &deadFirstBrowser{Fake: sandbox.NewFake(), failClose: true}

	st := runWith(t, cfg, adapter)
	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	if rows[0].Outcome == model.OutcomeSuccess {
		t.Fatalf("session 1 succeeded although its context could not be closed")
	}
	if rows[1].Outcome != model.OutcomeSuccess {
		t.Fatalf("session 2 ended %q (%s), want success on a second instance",
			rows[1].Outcome, rows[1].Error)
	}
	if rows[0].Instance == rows[1].Instance {
		t.Fatalf("both sessions used %s: the instance that lost its browser stayed in the pool",
			rows[0].Instance)
	}
}
