package run_test

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/adapter"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/report"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/run"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/store"
)

func config(policy model.Policy, sessions int, hold time.Duration) model.RunConfig {
	return model.RunConfig{
		RunID: "test", Sessions: sessions, Policy: policy, Namespace: "sandbox-demo",
		Pool: "browser-pool", Template: "browser-tpl", FixedArrival: true, Rate: 500,
		SampleSeconds: 1, OutDir: "out",
		Mix: []model.Bucket{{Label: hold.String(), Hold: hold, Count: sessions}},
	}
}

func execute(t *testing.T, cfg model.RunConfig, fake *adapter.Fake) (*store.Store, schedule.Plan) {
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
	runner := run.New(cfg, run.Options{
		Adapter: fake, Store: st, Plan: plan,
		Log: log.New(io.Discard, "", 0),
	})
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	return st, plan
}

func TestEverySessionGetsATerminalOutcome(t *testing.T) {
	cfg := config(model.PolicyDestroy, 24, 5*time.Millisecond)
	st, plan := execute(t, cfg, adapter.NewFake())

	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(rows) != len(plan.Sessions) {
		t.Fatalf("planned %d sessions, stored %d", len(plan.Sessions), len(rows))
	}
	ok, failed := 0, 0
	for _, row := range rows {
		switch row.Outcome {
		case model.OutcomeSuccess:
			ok++
		case model.OutcomePending:
			t.Fatalf("session %d is still pending", row.ID)
		case model.OutcomeWaiting:
		default:
			failed++
		}
	}
	if ok+failed != len(rows) {
		t.Fatalf("outcomes do not account for every session: ok=%d failed=%d total=%d", ok, failed, len(rows))
	}
	if ok != len(rows) {
		t.Fatalf("destroy policy with a healthy fake should succeed everywhere, got %d/%d", ok, len(rows))
	}
}

func TestReuseServesManySessionsOnOneInstance(t *testing.T) {
	cfg := config(model.PolicyReuse, 12, 5*time.Millisecond)
	st, _ := execute(t, cfg, adapter.NewFake())

	instances, err := st.Instances()
	if err != nil {
		t.Fatalf("instances: %v", err)
	}
	if len(instances) == 0 {
		t.Fatalf("no instances recorded")
	}
	var served int64
	for _, instance := range instances {
		if instance.Sessions > served {
			served = instance.Sessions
		}
	}
	if served < 2 {
		t.Fatalf("reuse policy never reused an instance: max sessions per instance = %d", served)
	}
}

func TestConcurrencyNeverExceedsInstances(t *testing.T) {
	// One instance is leased to one session at a time, so the measured peak can
	// never be larger than the number of instances the run created.
	cfg := config(model.PolicyReuse, 40, 20*time.Millisecond)
	st, plan := execute(t, cfg, adapter.NewFake())

	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	rep, err := report.Build(cfg, plan, st)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	instances, err := st.Instances()
	if err != nil {
		t.Fatalf("instances: %v", err)
	}
	if rep.Concurrency.Peak > len(instances) {
		t.Fatalf("peak concurrency %d exceeds the %d instances the run created", rep.Concurrency.Peak, len(instances))
	}
	if len(rows) != 40 {
		t.Fatalf("want 40 rows, got %d", len(rows))
	}
}

func TestRefusalIsRecordedAndAccounted(t *testing.T) {
	// A platform that refuses after four instances: every refusal is a row with
	// a reason, and the counts still balance.
	fake := adapter.NewFake()
	fake.RefuseAfter = 4
	// Overlapping sessions: the arrival rate and hold are chosen so the run
	// needs more instances at once than the fake will hand out.
	cfg := config(model.PolicyReuse, 20, 300*time.Millisecond)
	st, _ := execute(t, cfg, fake)

	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	refusals := 0
	for _, row := range rows {
		switch row.Outcome {
		case model.OutcomeAcquisitionFailed:
			refusals++
			if row.Error == "" {
				t.Fatalf("session %d failed without a reason", row.ID)
			}
		case model.OutcomeSuccess, model.OutcomeWaiting:
		default:
			t.Fatalf("session %d ended as %s: %s", row.ID, row.Outcome, row.Error)
		}
	}
	if refusals == 0 {
		t.Fatalf("the fake refused acquisitions but nothing was recorded as refused")
	}
}

func TestPoolReplicasAreRestored(t *testing.T) {
	fake := adapter.NewFake()
	cfg := config(model.PolicyDestroy, 2, time.Millisecond)
	replicas := int32(6)
	cfg.PoolReplicas = &replicas
	execute(t, cfg, fake)

	state, err := fake.Pool(context.Background(), cfg.Namespace, cfg.Pool)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	// NewFake starts at 1 desired; the run must leave it there.
	if state.Desired != 1 {
		t.Fatalf("pool left at desired=%d, want the original 1", state.Desired)
	}
}
