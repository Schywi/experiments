package slots_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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

// TestMaxProvisioningAdmitsKSessionsAtOnce is the property the capacity ramp
// depends on: with MaxProvisioning 1 the pool serialises cold paths, and with n it
// lets n of them run at the same time. Without this the fleet grows one pod per
// boot no matter how many sessions are waiting.
func TestMaxProvisioningAdmitsKSessionsAtOnce(t *testing.T) {
	const k = 3
	var mu sync.Mutex
	started, peak := 0, 0
	release := make(chan struct{})

	provide := func(ctx context.Context) (*sandbox.Instance, error) {
		mu.Lock()
		started++
		if started > peak {
			peak = started
		}
		mu.Unlock()
		<-release // hold the call open so concurrency is observable
		mu.Lock()
		started--
		mu.Unlock()
		return &sandbox.Instance{ID: "instance"}, nil
	}

	pool := slots.NewWithOptions(slots.Options{PerInstance: 1, Provide: provide, MaxProvisioning: k})
	if got := pool.MaxProvisioning(); got != k {
		t.Fatalf("MaxProvisioning %d, want %d", got, k)
	}

	done := make(chan struct{})
	for i := 0; i < k+1; i++ {
		go func() {
			_, _ = pool.Take(context.Background())
			done <- struct{}{}
		}()
	}

	// Wait until k creations are in flight, then check the (k+1)th is held back.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		running := started
		mu.Unlock()
		if running >= k || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	atPeak := peak
	mu.Unlock()
	if atPeak != k {
		t.Fatalf("%d creations ran at once, want %d", atPeak, k)
	}

	close(release)
	for i := 0; i < k+1; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a Take never returned")
		}
	}
}

// TestDefaultMaxProvisioningIsOne keeps the historical behaviour the default.
func TestDefaultMaxProvisioningIsOne(t *testing.T) {
	pool := slots.New(1, func(context.Context) (*sandbox.Instance, error) {
		return &sandbox.Instance{ID: "i"}, nil
	})
	if got := pool.MaxProvisioning(); got != 1 {
		t.Fatalf("default MaxProvisioning %d, want 1", got)
	}
}
