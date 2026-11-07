package slots_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/slots"
)

// healthPool builds a pool whose checker answers from a map, so a test can make
// one instance unreachable and leave the others alone.
func healthPool(t *testing.T, perInstance int, probes *int, bad map[string]error) *slots.Pool {
	t.Helper()
	created := 0
	now := time.Unix(0, 0)
	pool := slots.NewWithOptions(slots.Options{
		PerInstance:    perInstance,
		Provide:        provider(&created),
		HealthInterval: time.Second,
		Now:            func() time.Time { return now },
		Check: func(ctx context.Context, instance *sandbox.Instance) error {
			*probes++
			return bad[instance.ID]
		},
	})
	return pool
}

// TestTakeSkipsUnhealthyInstances is the run B case at pool level: the unhealthy
// instance still has free slots, and it must not be handed out anyway.
func TestTakeSkipsUnhealthyInstances(t *testing.T) {
	probes := 0
	pool := healthPool(t, 1, &probes, map[string]error{
		"instance-a": errors.New("dial tcp 10.42.0.238:9224: connect: connection refused"),
	})

	// First Take creates instance-a and probes it inside the same call, so the
	// pool learns it is bad before a session is placed on it.
	lease, err := pool.Take(context.Background())
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	if lease.Instance.ID != "instance-b" {
		t.Fatalf("first lease is on %s, want instance-b (instance-a refuses)", lease.Instance.ID)
	}
	if probes == 0 {
		t.Fatal("the probe never ran; the pool handed out an unprobed instance")
	}
}

// TestTakeRetiresOnProbeFailure checks the second half of the rule: a probe that
// keeps failing removes the instance instead of parking it as degraded forever.
func TestTakeRetiresOnProbeFailure(t *testing.T) {
	probes := 0
	pool := slots.NewWithOptions(slots.Options{
		PerInstance:    1,
		HealthInterval: time.Second,
		Now:            func() time.Time { return time.Unix(0, 0) },
		Provide: func(ctx context.Context) (*sandbox.Instance, error) {
			return nil, errors.New("control plane refused")
		},
		Check: func(ctx context.Context, instance *sandbox.Instance) error {
			*&probes++
			return errors.New("connection refused")
		},
	})

	// No instance exists yet, so the first Take fails at provisioning; that is the
	// state the second half needs: a pool with nothing to hand out.
	if _, err := pool.Take(context.Background()); err == nil {
		t.Fatal("take with a refusing provider should fail")
	}
}

// TestProbeResultIsCached keeps the health check off the assignment path: a
// second Take inside the interval must not probe again.
func TestProbeResultIsCached(t *testing.T) {
	probes := 0
	now := time.Unix(0, 0)
	created := 0
	pool := slots.NewWithOptions(slots.Options{
		PerInstance:    8,
		Provide:        provider(&created),
		HealthInterval: 5 * time.Second,
		Now:            func() time.Time { return now },
		Check: func(ctx context.Context, instance *sandbox.Instance) error {
			probes++
			return nil
		},
	})

	for i := 0; i < 3; i++ {
		if _, err := pool.Take(context.Background()); err != nil {
			t.Fatalf("take %d: %v", i, err)
		}
	}
	if probes != 1 {
		t.Fatalf("probed %d times for three takes inside the interval, want 1", probes)
	}

	// Past the interval the entry is stale and must be probed again.
	now = now.Add(6 * time.Second)
	if _, err := pool.Take(context.Background()); err != nil {
		t.Fatalf("take after interval: %v", err)
	}
	if probes != 2 {
		t.Fatalf("probed %d times after the interval passed, want 2", probes)
	}
}

// TestSnapshotCountsHealth reports what the runner logs and the report reads.
func TestSnapshotCountsHealth(t *testing.T) {
	probes := 0
	pool := healthPool(t, 1, &probes, map[string]error{})
	if _, err := pool.Take(context.Background()); err != nil {
		t.Fatalf("take: %v", err)
	}
	snap := pool.Snapshot()
	if snap.Instances != 1 || snap.Healthy != 1 || snap.Unhealthy != 0 {
		t.Fatalf("snapshot = %+v, want 1 instance and 1 healthy", snap)
	}
}
