package pool

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
)

func newScaler(t *testing.T, min, max int32) (*Scaler, *sandbox.Fake) {
	t.Helper()
	fake := sandbox.NewFake()
	scaler := New(Config{
		Namespace: "sandbox-demo", Pool: "browser-pool",
		Min: min, Max: max, Interval: time.Second,
		Log: log.New(io.Discard, "", 0),
	}, fake, nil, nil)
	return scaler, fake
}

func TestTargetFollowsPendingDemand(t *testing.T) {
	scaler, fake := newScaler(t, 1, 40)
	fake.Stats = sandbox.ClaimStats{Pending: 6, Ready: 4}
	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	// Demand is ready + waiting: ten instances would serve this instant.
	if settings.Target != 10 {
		t.Fatalf("target = %d, want 10", settings.Target)
	}
	state, _ := fake.Pool(context.Background(), "sandbox-demo", "browser-pool")
	if state.Desired != 10 {
		t.Fatalf("pool desired = %d, want the target 10", state.Desired)
	}
}

func TestConfiguredMaximumIsEnforced(t *testing.T) {
	scaler, fake := newScaler(t, 1, 8)
	fake.Stats = sandbox.ClaimStats{Pending: 30, Ready: 5}
	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if settings.Target != 8 {
		t.Fatalf("target = %d, want the configured max 8", settings.Target)
	}
	if settings.Desired != 8 {
		t.Fatalf("desired = %d, want 8", settings.Desired)
	}
}

func TestScaleDownWhenDemandDrops(t *testing.T) {
	scaler, fake := newScaler(t, 1, 20)
	if err := fake.SetPoolReplicas(context.Background(), "sandbox-demo", "browser-pool", 9); err != nil {
		t.Fatalf("seed replicas: %v", err)
	}
	fake.Stats = sandbox.ClaimStats{Pending: 0, Ready: 2}
	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if settings.Desired != 2 {
		t.Fatalf("desired = %d, want 2", settings.Desired)
	}
}

func TestMinimumHoldsWhenIdle(t *testing.T) {
	scaler, fake := newScaler(t, 3, 20)
	fake.Stats = sandbox.ClaimStats{}
	if _, err := scaler.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	state, _ := fake.Pool(context.Background(), "sandbox-demo", "browser-pool")
	if state.Desired != 3 {
		t.Fatalf("pool desired = %d, want the configured min 3", state.Desired)
	}
}

func TestNoChangeIsNotACounter(t *testing.T) {
	// A steady pool must not look like scaling: the target equals what is there.
	scaler, fake := newScaler(t, 1, 20)
	fake.Stats = sandbox.ClaimStats{Pending: 2, Ready: 3}
	if _, err := scaler.Step(context.Background()); err != nil {
		t.Fatalf("first step: %v", err)
	}
	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("second step: %v", err)
	}
	if settings.Target != settings.Desired {
		t.Fatalf("second step still wants to move: target %d desired %d", settings.Target, settings.Desired)
	}
}

func TestPausedPoolIsLeftAlone(t *testing.T) {
	// An experiment owns the replica count: the autoscaler must publish what it
	// sees and change nothing, or the two writers fight over spec.replicas.
	scaler, fake := newScaler(t, 1, 40)
	if err := fake.SetPoolReplicas(context.Background(), "sandbox-demo", "browser-pool", 7); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := fake.SetPoolPaused(context.Background(), "sandbox-demo", "browser-pool", true); err != nil {
		t.Fatalf("pause: %v", err)
	}
	fake.Stats = sandbox.ClaimStats{Pending: 12, Ready: 3}

	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if !settings.Paused {
		t.Fatalf("settings do not report the pool as paused")
	}
	state, _ := fake.Pool(context.Background(), "sandbox-demo", "browser-pool")
	if state.Desired != 7 {
		t.Fatalf("paused pool was resized to %d, want the experiment's 7", state.Desired)
	}
}

func TestDemandIsSlotsNotClaimsWhenBrowsersAreShared(t *testing.T) {
	// Twenty sessions at eight contexts per browser need three replicas, not
	// twenty. Scaling on claims inflates the pool by the packing factor and
	// leaves it there after the run.
	fake := sandbox.NewFake()
	scaler := New(Config{
		Namespace: "sandbox-demo", Pool: "browser-pool",
		Min: 1, Max: 40, Interval: time.Second, ContextsPerBrowser: 8,
		Log: log.New(io.Discard, "", 0),
	}, fake, nil, nil)
	fake.Stats = sandbox.ClaimStats{Ready: 20}

	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if settings.Target != 3 {
		t.Fatalf("target = %d, want ceil(20/8) = 3", settings.Target)
	}
	state, _ := fake.Pool(context.Background(), "sandbox-demo", "browser-pool")
	if state.Desired != 3 {
		t.Fatalf("pool desired = %d, want 3", state.Desired)
	}
}

func TestBulkDemandDoesNotGrowThePool(t *testing.T) {
	// Bulk work uses capacity that already exists. Counting it would let a bulk
	// crawl inflate the pool and hold replicas nothing else asked for.
	scaler, fake := newScaler(t, 2, 40)
	fake.Stats = sandbox.ClaimStats{Pending: 3, Failed: 1, Bulk: 25}

	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if settings.Target != 3 {
		t.Fatalf("target = %d, want ready+pending = 3 with the 25 bulk claims ignored", settings.Target)
	}
}

func TestPoolDoesNotGrowFasterThanBrowsersCanBoot(t *testing.T) {
	// Pending pods hold requests that starve the scheduler. When the runner says it
	// can boot two at a time, the pool grows by two per reconcile at most.
	fake := sandbox.NewFake()
	if err := fake.SetPoolReplicas(context.Background(), "sandbox-demo", "browser-pool", 2); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := fake.SetBootConcurrency(context.Background(), "sandbox-demo", "browser-pool", 2); err != nil {
		t.Fatalf("publish: %v", err)
	}
	fake.Stats = sandbox.ClaimStats{Ready: 2, Pending: 40}

	scaler := New(Config{
		Namespace: "sandbox-demo", Pool: "browser-pool", Min: 2, Max: 40,
		Interval: time.Second, Log: log.New(io.Discard, "", 0),
	}, fake, nil, nil)
	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if settings.Target != 4 {
		t.Fatalf("target = %d, want 4 (current 2 + boot limit 2) even with 42 sessions queued",
			settings.Target)
	}
}

func nodeLoadScaler(t *testing.T, min, max int32, load NodeLoadFunc) (*Scaler, *sandbox.Fake) {
	t.Helper()
	fake := sandbox.NewFake()
	scaler := New(Config{
		Namespace: "sandbox-demo", Pool: "browser-pool",
		Min: min, Max: max, Interval: time.Second, NodeLoad: load,
		Log: log.New(io.Discard, "", 0),
	}, fake, nil, nil)
	return scaler, fake
}

func TestPressureSaturationShrinksDespiteDemand(t *testing.T) {
	// A saturated node shrinks by one step even with a queue: the queue cannot
	// be served until the node has headroom, and adding pods starves it further.
	scaler, fake := nodeLoadScaler(t, 1, 40, func() (NodeLoad, error) {
		return NodeLoad{Utilization: 0.9, PressureFull: 40}, nil
	})
	if err := fake.SetPoolReplicas(context.Background(), "sandbox-demo", "browser-pool", 10); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fake.Stats = sandbox.ClaimStats{Ready: 10, Pending: 30}

	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if settings.Target != 9 {
		t.Fatalf("target = %d, want 9 (one step down from 10 despite demand)", settings.Target)
	}
	if settings.PressureFull != 40 || settings.Utilization != 0.9 {
		t.Fatalf("settings do not carry the load: %+v", settings)
	}
}

func TestPressureFreeGrowsTowardDemand(t *testing.T) {
	scaler, fake := nodeLoadScaler(t, 1, 40, func() (NodeLoad, error) {
		return NodeLoad{Utilization: 0.1, PressureFull: 1}, nil
	})
	fake.Stats = sandbox.ClaimStats{Ready: 4, Pending: 8}
	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if settings.Target != 12 {
		t.Fatalf("target = %d, want 12 (demand) on a free node", settings.Target)
	}
}

func TestPressureBandHolds(t *testing.T) {
	// Inside the hysteresis band the autoscaler holds, so it cannot oscillate
	// every reconcile even when demand says grow.
	scaler, fake := nodeLoadScaler(t, 1, 40, func() (NodeLoad, error) {
		return NodeLoad{Utilization: 0.75, PressureFull: 10}, nil
	})
	if err := fake.SetPoolReplicas(context.Background(), "sandbox-demo", "browser-pool", 6); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fake.Stats = sandbox.ClaimStats{Ready: 6, Pending: 14}
	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if settings.Target != 6 {
		t.Fatalf("target = %d, want 6 (hold in the band despite demand 20)", settings.Target)
	}
}

func TestNodeLoadErrorFallsBackToDemand(t *testing.T) {
	scaler, fake := nodeLoadScaler(t, 1, 40, func() (NodeLoad, error) {
		return NodeLoad{}, errNodeLoad
	})
	fake.Stats = sandbox.ClaimStats{Ready: 2, Pending: 10}
	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if settings.Target != 12 {
		t.Fatalf("target = %d, want 12 (demand) when node load is unavailable", settings.Target)
	}
}

var errNodeLoad = &nodeLoadError{}

type nodeLoadError struct{}

func (e *nodeLoadError) Error() string { return "no /proc" }

// publishedScaler builds a scaler whose demand comes from the runner's counts.
func publishedScaler(t *testing.T, min, max int32, live int, ok bool) (*Scaler, *sandbox.Fake) {
	t.Helper()
	fake := sandbox.NewFake()
	scaler := New(Config{
		Namespace: "sandbox-demo", Pool: "browser-pool",
		Min: min, Max: max, Interval: time.Second,
		Log:          log.New(io.Discard, "", 0),
		DemandSource: "published",
		DemandReader: func(context.Context) (Demand, bool, error) {
			if !ok {
				return Demand{}, false, nil
			}
			return Demand{Live: live, Contexts: 8}, true, nil
		},
		ImageTag: "v2", MaxProvisioning: 4,
	}, fake, nil, nil)
	return scaler, fake
}

func TestPublishedDemandSizesTheFleetFromSessions(t *testing.T) {
	// Claims cannot see these sessions: nothing is Pending because the runner
	// serialises cold paths. The published count says 800 live sessions, which at
	// 8 contexts per browser is 100 replicas.
	scaler, fake := publishedScaler(t, 1, 200, 800, true)
	fake.Stats = sandbox.ClaimStats{Ready: 4, Pending: 0}

	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if settings.DemandSource != "published" || settings.DemandFallback {
		t.Fatalf("demand source %q fallback %v, want published and no fallback",
			settings.DemandSource, settings.DemandFallback)
	}
	if settings.DemandPods != 100 || settings.Target != 100 {
		t.Fatalf("demand %d target %d, want 100 and 100", settings.DemandPods, settings.Target)
	}
	if settings.ImageTag != "v2" || settings.MaxProvisioning != 4 {
		t.Fatalf("settings do not identify the arm: %+v", settings)
	}
}

func TestPublishedDemandFallsBackToClaimsAndSaysSo(t *testing.T) {
	// No publish has arrived yet: size on claims, and mark the settings so a
	// report cannot read a fallback as a published number.
	scaler, fake := publishedScaler(t, 1, 200, 0, false)
	fake.Stats = sandbox.ClaimStats{Ready: 4, Pending: 8}

	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if !settings.DemandFallback {
		t.Fatalf("fallback not reported: %+v", settings)
	}
	if settings.DemandSource != "claims" {
		t.Fatalf("demand source %q, want claims", settings.DemandSource)
	}
	if settings.Target != 12 {
		t.Fatalf("target %d, want 12 from claims", settings.Target)
	}
}

func TestMemoryReserveHoldsGrowthEvenWhenTheNodeLooksFree(t *testing.T) {
	// The failure this closes: a CPU-free node at 89 % memory grew toward demand.
	fake := sandbox.NewFake()
	scaler := New(Config{
		Namespace: "sandbox-demo", Pool: "browser-pool",
		Min: 1, Max: 200, Interval: time.Second,
		Log:             log.New(io.Discard, "", 0),
		MemoryReserveMB: 4096,
		NodeLoad: func() (NodeLoad, error) {
			return NodeLoad{Utilization: 0.1, PressureFull: 1, MemAvailableMB: 2048, MemTotalMB: 16384}, nil
		},
	}, fake, nil, nil)
	if err := fake.SetPoolReplicas(context.Background(), "sandbox-demo", "browser-pool", 10); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fake.Stats = sandbox.ClaimStats{Ready: 10, Pending: 30}

	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if settings.Target != 9 {
		t.Fatalf("target %d, want 9: available memory is below the reserve, so the pool relieves the node", settings.Target)
	}
	if settings.MemAvailableMB != 2048 {
		t.Fatalf("settings do not carry memory: %+v", settings)
	}
}

func TestMemoryPressureTermHoldsInsideTheCpuBand(t *testing.T) {
	// CPU inside its hysteresis band would hold anyway; memory pressure high must
	// also shrink, because that is the term the CPU guard cannot see.
	fake := sandbox.NewFake()
	scaler := New(Config{
		Namespace: "sandbox-demo", Pool: "browser-pool",
		Min: 1, Max: 200, Interval: time.Second,
		Log:             log.New(io.Discard, "", 0),
		MemPressureHigh: 10,
		NodeLoad: func() (NodeLoad, error) {
			return NodeLoad{Utilization: 0.75, PressureFull: 10, MemoryPressureFull: 25}, nil
		},
	}, fake, nil, nil)
	if err := fake.SetPoolReplicas(context.Background(), "sandbox-demo", "browser-pool", 10); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fake.Stats = sandbox.ClaimStats{Ready: 10, Pending: 30}

	settings, err := scaler.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if settings.Target != 9 {
		t.Fatalf("target %d, want 9 on high memory pressure", settings.Target)
	}
}
