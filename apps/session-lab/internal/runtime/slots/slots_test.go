package slots_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/slots"
)

func provider(counter *int) slots.Provider {
	var mu sync.Mutex
	return func(ctx context.Context) (*sandbox.Instance, error) {
		mu.Lock()
		defer mu.Unlock()
		*counter++
		return &sandbox.Instance{ID: "instance-" + string(rune('a'+*counter-1))}, nil
	}
}

func TestOneBrowserServesManySessions(t *testing.T) {
	// Eight contexts per browser: eight sessions, one instance.
	acquired := 0
	pool := slots.New(8, provider(&acquired))

	leases := make([]slots.Lease, 8)
	for i := range leases {
		lease, err := pool.Take(context.Background())
		if err != nil {
			t.Fatalf("take %d: %v", i, err)
		}
		leases[i] = lease
	}
	if acquired != 1 {
		t.Fatalf("created %d instances for 8 slots, want 1", acquired)
	}
	for i, lease := range leases {
		if lease.Instance.ID != leases[0].Instance.ID {
			t.Fatalf("lease %d is on %s, want the same browser as the others", i, lease.Instance.ID)
		}
		if !lease.Shared {
			t.Fatalf("lease %d does not report a shared instance", i)
		}
	}

	// The ninth session needs a second browser: the first is full.
	extra, err := pool.Take(context.Background())
	if err != nil {
		t.Fatalf("take extra: %v", err)
	}
	if acquired != 2 {
		t.Fatalf("created %d instances, want 2 after the ninth session", acquired)
	}
	if extra.Instance.ID == leases[0].Instance.ID {
		t.Fatalf("the ninth session was packed into a full browser")
	}

	snap := pool.Snapshot()
	if snap.Instances != 2 || snap.Slots != 16 || snap.InUse != 9 {
		t.Fatalf("snapshot = %+v, want 2 instances, 16 slots, 9 in use", snap)
	}
}

func TestFreedSlotIsReusedBeforeCapacityIsRequested(t *testing.T) {
	acquired := 0
	pool := slots.New(4, provider(&acquired))
	ctx := context.Background()

	first, err := pool.Take(ctx)
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	if idle := pool.Give(first); !idle {
		t.Fatalf("the only instance should be idle after its slot is returned")
	}

	again, err := pool.Take(ctx)
	if err != nil {
		t.Fatalf("take again: %v", err)
	}
	if again.Instance.ID != first.Instance.ID {
		t.Fatalf("a freed slot was not reused: got %s, want %s", again.Instance.ID, first.Instance.ID)
	}
	if acquired != 1 {
		t.Fatalf("acquired %d instances for one reused slot, want 1", acquired)
	}
}

func TestProviderFailureIsReportedNotSwallowed(t *testing.T) {
	pool := slots.New(2, func(ctx context.Context) (*sandbox.Instance, error) {
		return nil, errors.New("0/1 nodes are available: 1 Insufficient cpu")
	})
	if _, err := pool.Take(context.Background()); err == nil {
		t.Fatalf("a refused capacity request was reported as a slot")
	}
	if snap := pool.Snapshot(); snap.Instances != 0 || snap.InUse != 0 {
		t.Fatalf("a failed acquisition left state behind: %+v", snap)
	}
}

func TestRetireDropsAnInstanceFromThePool(t *testing.T) {
	acquired := 0
	pool := slots.New(1, provider(&acquired))
	lease, err := pool.Take(context.Background())
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	pool.Retire(lease.Instance)
	if snap := pool.Snapshot(); snap.Instances != 0 || snap.InUse != 0 {
		t.Fatalf("retire left state behind: %+v", snap)
	}
}

func TestInUseOnCountsTheSessionsSharingOneBrowser(t *testing.T) {
	acquired := 0
	pool := slots.New(4, provider(&acquired))
	ctx := context.Background()
	first, _ := pool.Take(ctx)
	second, _ := pool.Take(ctx)
	if got := pool.InUseOn(first.Instance); got != 2 {
		t.Fatalf("InUseOn = %d, want 2 — this is the divisor for per-session CPU", got)
	}
	pool.Give(second)
	if got := pool.InUseOn(first.Instance); got != 1 {
		t.Fatalf("InUseOn = %d after a release, want 1", got)
	}
}
