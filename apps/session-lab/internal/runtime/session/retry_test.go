package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// refusingInstances fails the browser-context call for the first n instances it
// is asked to open a context on, with the error run B produced.
type refusingInstances struct {
	*sandbox.Fake
	refuse   int
	seen     map[string]bool
	attempts int
}

func newRefusingInstances(refuse int) *refusingInstances {
	return &refusingInstances{Fake: sandbox.NewFake(), refuse: refuse, seen: map[string]bool{}}
}

func (a *refusingInstances) OpenSession(ctx context.Context, instance *sandbox.Instance, url string) (sandbox.BrowserSession, error) {
	a.attempts++
	if len(a.seen) < a.refuse {
		a.seen[instance.ID] = true
		return sandbox.BrowserSession{}, errors.New(
			"discover http://10.42.0.238:9224: dial tcp 10.42.0.238:9224: connect: connection refused")
	}
	return a.Fake.OpenSession(ctx, instance, url)
}

// TestSessionRetriesOnceOnATransportFailure: one refusal must not end the
// session. The retry runs on a different instance, because the failed one was
// retired by the previous phase.
func TestSessionRetriesOnceOnATransportFailure(t *testing.T) {
	cfg := config(model.PolicyReuse, 1, 10*time.Millisecond)
	cfg.ContextsPerBrowser = 8
	cfg.Retries = 1
	adapter := newRefusingInstances(1)

	st := runWith(t, cfg, adapter)
	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	if rows[0].Outcome != model.OutcomeSuccess {
		t.Fatalf("session ended %q (%s), want success after one retry", rows[0].Outcome, rows[0].Error)
	}
	if rows[0].Attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (the first refused, the second worked)", rows[0].Attempts)
	}
}

// TestSessionDoesNotRetryANonTransportFailure keeps the retry narrow: a browser
// that answered with a protocol error is not a broken connection.
func TestSessionDoesNotRetryANonTransportFailure(t *testing.T) {
	cfg := config(model.PolicyReuse, 1, 10*time.Millisecond)
	cfg.ContextsPerBrowser = 8
	cfg.Retries = 1

	fake := sandbox.NewFake()
	fake.OpenSessionErr = errors.New("cdp: unexpected reply to Target.createTarget")
	st := runWith(t, cfg, fake)

	rows, _ := st.Sessions()
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	if rows[0].Outcome != model.OutcomeResetFailed {
		t.Fatalf("session ended %q, want reset_failed", rows[0].Outcome)
	}
	if rows[0].Attempts != 1 {
		t.Fatalf("attempts = %d, want 1: a protocol error is not retried", rows[0].Attempts)
	}
}

// TestSessionRetryIsBounded: when every instance refuses, the session fails after
// the budget instead of looping.
func TestSessionRetryIsBounded(t *testing.T) {
	cfg := config(model.PolicyReuse, 1, 10*time.Millisecond)
	cfg.ContextsPerBrowser = 8
	cfg.Retries = 1
	adapter := newRefusingInstances(99)

	st := runWith(t, cfg, adapter)
	rows, _ := st.Sessions()
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	if rows[0].Outcome != model.OutcomeResetFailed {
		t.Fatalf("session ended %q, want reset_failed", rows[0].Outcome)
	}
	if rows[0].Attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (one try plus one retry)", rows[0].Attempts)
	}
}

// TestRetriesDisabledMeansOneAttempt pins the off switch.
func TestRetriesDisabledMeansOneAttempt(t *testing.T) {
	cfg := config(model.PolicyReuse, 1, 10*time.Millisecond)
	cfg.ContextsPerBrowser = 8
	cfg.Retries = 0
	adapter := newRefusingInstances(1)

	st := runWith(t, cfg, adapter)
	rows, _ := st.Sessions()
	if rows[0].Outcome != model.OutcomeResetFailed {
		t.Fatalf("session ended %q, want reset_failed with retries off", rows[0].Outcome)
	}
	if rows[0].Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", rows[0].Attempts)
	}
}
