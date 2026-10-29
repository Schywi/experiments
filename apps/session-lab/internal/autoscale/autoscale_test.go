package autoscale

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/adapter"
)

func newScaler(t *testing.T, min, max int32) (*Scaler, *adapter.Fake) {
	t.Helper()
	fake := adapter.NewFake()
	scaler := New(Config{
		Namespace: "sandbox-demo", Pool: "browser-pool",
		Min: min, Max: max, Interval: time.Second,
		Log: log.New(io.Discard, "", 0),
	}, fake, nil, nil)
	return scaler, fake
}

func TestTargetFollowsPendingDemand(t *testing.T) {
	scaler, fake := newScaler(t, 1, 40)
	fake.Stats = adapter.ClaimStats{Pending: 6, Ready: 4}
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
	fake.Stats = adapter.ClaimStats{Pending: 30, Ready: 5}
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
	fake.Stats = adapter.ClaimStats{Pending: 0, Ready: 2}
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
	fake.Stats = adapter.ClaimStats{}
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
	fake.Stats = adapter.ClaimStats{Pending: 2, Ready: 3}
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
	fake.Stats = adapter.ClaimStats{Pending: 12, Ready: 3}

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
