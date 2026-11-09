package queue

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// order records the sequence permits were granted in.
type order struct {
	mu   sync.Mutex
	seen []int64
}

func (o *order) add(id int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, id)
}

func (o *order) ids() []int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]int64, len(o.seen))
	copy(out, o.seen)
	return out
}

// waitDepth blocks until the gate holds n waiters, so a test can be sure the
// requests it cares about are queued rather than still being scheduled.
func waitDepth(t *testing.T, gate *Live, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if gate.Depth() == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("gate depth settled at %d, want %d", gate.Depth(), n)
}

func TestLiveGrantsByClassWithOnePermit(t *testing.T) {
	gate := NewLive(NewPriority(), 1)
	// One permit is taken and held, so none of the requests below is served on
	// arrival: a work-conserving gate would otherwise grant the first one to
	// show up, whichever goroutine that was.
	if err := gate.Acquire(context.Background(), Request{ID: 99, Class: model.ClassNormal}); err != nil {
		t.Fatalf("holding the permit: %v", err)
	}
	var got order
	var wg sync.WaitGroup
	// Offered in this order, so arrival order and class order disagree.
	requests := []Request{
		{ID: 1, Class: model.ClassBulk},
		{ID: 2, Class: model.ClassNormal},
		{ID: 3, Class: model.ClassBatch},
	}
	for _, req := range requests {
		wg.Add(1)
		go func(req Request) {
			defer wg.Done()
			if err := gate.Acquire(context.Background(), req); err != nil {
				t.Errorf("acquire %d: %v", req.ID, err)
				return
			}
			got.add(req.ID)
			// Hold the permit long enough that the others are certainly waiting.
			time.Sleep(2 * time.Millisecond)
			gate.Release(req)
		}(req)
	}
	waitDepth(t, gate, 3)
	gate.Release(Request{ID: 99})
	wg.Wait()
	want := []int64{2, 3, 1}
	ids := got.ids()
	if len(ids) != len(want) {
		t.Fatalf("granted %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("granted %v, want %v", ids, want)
		}
	}
}

func TestLiveHonoursTheContext(t *testing.T) {
	gate := NewLive(NewFIFO(), 1)
	// Take the only permit and keep it.
	if err := gate.Acquire(context.Background(), Request{ID: 1, Class: model.ClassNormal}); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	err := gate.Acquire(ctx, Request{ID: 2, Class: model.ClassNormal})
	if err == nil {
		t.Fatalf("a waiting acquire returned without a permit")
	}
	if gate.Depth() != 0 {
		t.Fatalf("the waiter was not removed: depth %d", gate.Depth())
	}
	gate.Release(Request{ID: 1})
	// The gate still works after an abandoned waiter.
	if err := gate.Acquire(context.Background(), Request{ID: 3, Class: model.ClassNormal}); err != nil {
		t.Fatalf("acquire after abandon: %v", err)
	}
	gate.Release(Request{ID: 3})
}

func TestLiveBoundsConcurrency(t *testing.T) {
	const limit = 2
	gate := NewLive(NewFIFO(), limit)
	var wg sync.WaitGroup
	var mu sync.Mutex
	inFlight, peak := 0, 0
	for i := int64(0); i < 8; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			if err := gate.Acquire(context.Background(), Request{ID: id, Class: model.ClassNormal}); err != nil {
				t.Errorf("acquire %d: %v", id, err)
				return
			}
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			inFlight--
			mu.Unlock()
			gate.Release(Request{ID: id, Class: model.ClassNormal})
		}(i)
	}
	wg.Wait()
	if peak > limit {
		t.Fatalf("%d sessions held a permit at once, limit is %d", peak, limit)
	}
	if gate.Depth() != 0 {
		t.Fatalf("gate depth %d after every release", gate.Depth())
	}
}
