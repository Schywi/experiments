package slots_test

import (
	"context"
	"testing"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/slots"
)

// TestRetireAndGiveRemovesTheEntry is the phase-1 invariant: a browser that has
// failed leaves the pool in one call, and the slot it was holding is not handed
// out again.
func TestRetireAndGiveRemovesTheEntry(t *testing.T) {
	created := 0
	pool := slots.New(8, provider(&created))

	lease, err := pool.Take(context.Background())
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	if snap := pool.Snapshot(); snap.Instances != 1 || snap.InUse != 1 {
		t.Fatalf("snapshot after take = %+v, want 1 instance and 1 slot in use", snap)
	}

	pool.RetireAndGive(lease)

	snap := pool.Snapshot()
	if snap.Instances != 0 {
		t.Fatalf("snapshot after RetireAndGive = %+v, want the instance gone", snap)
	}
	if snap.InUse != 0 {
		t.Fatalf("InUse = %d after RetireAndGive, want 0", snap.InUse)
	}

	// The next Take must not return the retired browser.
	next, err := pool.Take(context.Background())
	if err != nil {
		t.Fatalf("take after retire: %v", err)
	}
	if next.Instance.ID == lease.Instance.ID {
		t.Fatalf("Take returned the retired instance %s", next.Instance.ID)
	}
	if created != 2 {
		t.Fatalf("created %d instances, want a second one after the retire", created)
	}
}

// TestRetireAndGiveIsIdempotent guards the double-failure path: a session that
// fails at isolate and again at close must not drive the counters negative.
func TestRetireAndGiveIsIdempotent(t *testing.T) {
	created := 0
	pool := slots.New(8, provider(&created))
	lease, err := pool.Take(context.Background())
	if err != nil {
		t.Fatalf("take: %v", err)
	}

	pool.RetireAndGive(lease)
	pool.RetireAndGive(lease)

	if snap := pool.Snapshot(); snap.InUse != 0 || snap.Instances != 0 {
		t.Fatalf("snapshot after two RetireAndGive = %+v, want 0/0", snap)
	}

	// A lease on an instance that is already gone must be harmless too.
	orphan := slots.Lease{Instance: &sandbox.Instance{ID: "never-existed"}}
	pool.RetireAndGive(orphan)
	if snap := pool.Snapshot(); snap.InUse != 0 {
		t.Fatalf("InUse = %d after retiring an unknown instance, want 0", snap.InUse)
	}
}
