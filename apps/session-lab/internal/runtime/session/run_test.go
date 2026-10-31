package session_test

import (
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/measure"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/report"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/session"
)

func config(policy model.Policy, sessions int, hold time.Duration) model.RunConfig {
	return model.RunConfig{
		RunID: "test", Sessions: sessions, Policy: policy, Namespace: "sandbox-demo",
		Pool: "browser-pool", Template: "browser-tpl", FixedArrival: true, Rate: 500,
		SampleSeconds: 1, OutDir: "out",
		Mix: []model.Bucket{{Label: hold.String(), Hold: hold, Count: sessions}},
	}
}

func execute(t *testing.T, cfg model.RunConfig, fake *sandbox.Fake) (*store.Store, schedule.Plan) {
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
	st, plan := execute(t, cfg, sandbox.NewFake())

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
	st, _ := execute(t, cfg, sandbox.NewFake())

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
	st, plan := execute(t, cfg, sandbox.NewFake())

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
	fake := sandbox.NewFake()
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
	fake := sandbox.NewFake()
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

func TestTemplateLimitsAreRecorded(t *testing.T) {
	// A throttling count is unreadable without the limit that caused it, so the
	// run row carries the template's requests and limits.
	fake := sandbox.NewFake()
	fake.Limits = sandbox.TemplateLimits{
		Container: "browser", CPURequestMilli: 100, CPULimitMilli: 1000,
		MemRequestBytes: 256 << 20, MemLimitBytes: 768 << 20,
	}
	cfg := config(model.PolicyDestroy, 2, time.Millisecond)
	st, _ := execute(t, cfg, fake)

	raw, err := st.TemplateLimits(cfg.RunID)
	if err != nil {
		t.Fatalf("template limits: %v", err)
	}
	if raw == "" {
		t.Fatalf("the run row has no template limits")
	}
	for _, want := range []string{"browser", "1000", "805306368"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("recorded limits are missing %q: %s", want, raw)
		}
	}
}

func TestOneBrowserServesSeveralSessions(t *testing.T) {
	// Eight contexts per browser: sixteen overlapping sessions must fit in two
	// instances, not sixteen. This is the whole point of the shared path.
	cfg := config(model.PolicyReuse, 16, 30*time.Millisecond)
	cfg.ContextsPerBrowser = 8
	cfg.Rate = 2000
	fake := sandbox.NewFake()
	st, plan := execute(t, cfg, fake)

	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(rows) != len(plan.Sessions) {
		t.Fatalf("planned %d, stored %d", len(plan.Sessions), len(rows))
	}
	instances := map[string]bool{}
	contexts := map[string]bool{}
	for _, row := range rows {
		if row.Outcome != model.OutcomeSuccess {
			t.Fatalf("session %d ended %q: %s", row.ID, row.Outcome, row.Error)
		}
		instances[row.Instance] = true
		if row.BrowserContext == "" {
			t.Fatalf("session %d has no browser context", row.ID)
		}
		contexts[row.BrowserContext] = true
	}
	if len(contexts) != len(rows) {
		t.Fatalf("%d sessions shared %d contexts; each session needs its own", len(rows), len(contexts))
	}
	if len(instances) > 3 {
		t.Fatalf("%d sessions used %d instances, want at most 3 with eight slots each",
			len(rows), len(instances))
	}
	if len(instances) < 2 {
		t.Fatalf("16 sessions fit in %d instance(s); slots were not respected", len(instances))
	}
}

func TestSharedInstanceCpuIsDividedByContexts(t *testing.T) {
	// With several sessions in one browser, the pod counter belongs to all of
	// them: the per-session figure is the pod's usage divided by the contexts
	// that were in flight, and the row says so.
	cfg := config(model.PolicyReuse, 4, 40*time.Millisecond)
	cfg.ContextsPerBrowser = 4
	cfg.Rate = 2000
	st, _ := execute(t, cfg, sandbox.NewFake())

	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	shared := 0
	for _, row := range rows {
		if row.CPUShared {
			shared++
		}
	}
	if shared == 0 {
		t.Fatalf("no session reported a shared browser, so the CPU figure is presented as exclusive")
	}
}

// samplingAdapter reports readings for every sample, which is what a real pod
// does. It exists because a session that finishes its hold while being sampled
// was once recorded as interrupted: the two booleans returned by hold() were
// swapped in the shared-browser path.
type samplingAdapter struct {
	*sandbox.Fake
}

func (s *samplingAdapter) WaitReady(ctx context.Context, instance *sandbox.Instance) error {
	if err := s.Fake.WaitReady(ctx, instance); err != nil {
		return err
	}
	instance.PodUID = "pod-" + instance.ID
	return nil
}

type fixedSampler struct{}

func (fixedSampler) Sample(ctx context.Context, podUID, node string) (measure.Reading, bool, error) {
	return measure.Reading{CPUUsec: 1000, MemCurrent: 1024}, true, nil
}

func TestCompletedHoldWithSamplesIsSuccess(t *testing.T) {
	cfg := config(model.PolicyReuse, 4, 20*time.Millisecond)
	cfg.ContextsPerBrowser = 4
	cfg.Rate = 2000
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
		Adapter: &samplingAdapter{Fake: sandbox.NewFake()},
		Store:   st, Plan: plan, Sampler: fixedSampler{},
		Log: log.New(io.Discard, "", 0),
	})
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	for _, row := range rows {
		if row.Outcome != model.OutcomeSuccess {
			t.Fatalf("session %d ended %q (%s) after completing a %s hold with samples",
				row.ID, row.Outcome, row.Error, row.Bucket)
		}
		if row.CPUUsec == 0 {
			t.Fatalf("session %d recorded no CPU from its samples", row.ID)
		}
	}
}

func TestTransientClaimFailureIsRetried(t *testing.T) {
	// A control plane that answers one POST with EOF and then works: the session
	// must succeed, because a transport error is not a session outcome.
	cfg := config(model.PolicyReuse, 2, 20*time.Millisecond)
	cfg.ContextsPerBrowser = 4
	cfg.Rate = 2000
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
	fake.AcquireErrors = []error{
		errors.New(`Post "https://127.0.0.1:6443/apis/extensions.agents.x-k8s.io/v1beta1/namespaces/sandbox-demo/sandboxclaims": unexpected EOF`),
	}
	runner := session.New(cfg, session.Options{
		Adapter: fake, Store: st, Plan: plan,
		Log: log.New(io.Discard, "", 0),
	})
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	rows, _ := st.Sessions()
	for _, row := range rows {
		if row.Outcome != model.OutcomeSuccess {
			t.Fatalf("session %d ended %q (%s); a transient EOF should have been retried",
				row.ID, row.Outcome, row.Error)
		}
	}

	// And the same failure on every attempt still fails, without hanging.
	cfg2 := config(model.PolicyReuse, 1, 10*time.Millisecond)
	cfg2.ContextsPerBrowser = 2
	plan2, _ := schedule.Generate(cfg2, 1)
	st2, _ := store.Open(filepath.Join(t.TempDir(), "run2.db"))
	t.Cleanup(func() { st2.Close() })
	failing := sandbox.NewFake()
	failing.AcquireErrors = []error{
		errors.New("unexpected EOF"), errors.New("unexpected EOF"), errors.New("unexpected EOF"),
		errors.New("unexpected EOF"), errors.New("unexpected EOF"),
	}
	runner2 := session.New(cfg2, session.Options{Adapter: failing, Store: st2, Plan: plan2, Log: log.New(io.Discard, "", 0)})
	runner2.Run(context.Background())
	rows2, _ := st2.Sessions()
	if len(rows2) != 1 || rows2[0].Outcome != model.OutcomeAcquisitionFailed {
		t.Fatalf("a persistent failure should end acquisition_failed, got %+v", rows2)
	}
}

func TestSlotWaitIsMeasuredOnTheSharedPath(t *testing.T) {
	// Arrival -> usable slot is the number the experiment exists to measure; the
	// shared path used to stamp the browser's own readiness, making it zero.
	cfg := config(model.PolicyReuse, 4, 30*time.Millisecond)
	cfg.ContextsPerBrowser = 2
	cfg.Rate = 2000
	st, _ := execute(t, cfg, sandbox.NewFake())
	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	measured := 0
	for _, row := range rows {
		if row.ReadyNS > 0 && row.ClaimCreatedNS > 0 && row.ReadyNS >= row.ClaimCreatedNS {
			measured++
		}
	}
	if measured != len(rows) {
		t.Fatalf("%d of %d sessions have no slot wait measured", measured, len(rows))
	}
}

func TestFixedPoolRunTakesAndReleasesPoolOwnership(t *testing.T) {
	// A run that sets a replica count must tell any autoscaler to stand down, and
	// must release it afterwards even if the run ends badly — otherwise the pool
	// stays paused and silent forever, which is what happened twice already.
	fixed := int32(3)
	cfg := config(model.PolicyReuse, 4, 10*time.Millisecond)
	cfg.PoolReplicas = &fixed
	fake := sandbox.NewFake()
	execute(t, cfg, fake)

	history := fake.OwnershipHistory()
	if len(history) < 2 {
		t.Fatalf("pool ownership toggled %d times (%v), want at least take+release", len(history), history)
	}
	if history[0] != true {
		t.Fatalf("first ownership call was paused=%v, want true before the run", history[0])
	}
	if history[len(history)-1] != false {
		t.Fatalf("last ownership call was paused=%v, want false after the run", history[len(history)-1])
	}
}

func TestElasticRunLeavesPoolOwnershipAlone(t *testing.T) {
	cfg := config(model.PolicyReuse, 3, 10*time.Millisecond)
	fake := sandbox.NewFake()
	execute(t, cfg, fake)

	history := fake.OwnershipHistory()
	if len(history) != 0 {
		t.Fatalf("a run without --pool-replicas touched pool ownership: %v", history)
	}
}
