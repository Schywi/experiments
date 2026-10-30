package admission_test

import (
	"context"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/admission"
)

func TestClassesOrderTheQueueWithoutRejecting(t *testing.T) {
	// Nothing here refuses a session; the classes only decide who goes first.
	if model.ClassNormal.Rank() >= model.ClassBatch.Rank() || model.ClassBatch.Rank() >= model.ClassBulk.Rank() {
		t.Fatalf("class ranks are not ordered: %d %d %d",
			model.ClassNormal.Rank(), model.ClassBatch.Rank(), model.ClassBulk.Rank())
	}
	if model.ClassBulk.MayScale() {
		t.Fatalf("bulk work may not grow the pool")
	}
	if !model.ClassNormal.MayScale() || !model.ClassBatch.MayScale() {
		t.Fatalf("normal and batch work may grow the pool")
	}
	if _, err := model.ParseClass("urgent"); err == nil {
		t.Fatalf("an unknown class was accepted")
	}
	if got, err := model.ParseClass(""); err != nil || got != model.ClassNormal {
		t.Fatalf("empty class = %q, %v; want normal", got, err)
	}
}

func TestQueueCountsPerClass(t *testing.T) {
	q := admission.NewQueue()
	q.Push("a", model.ClassNormal)
	q.Push("b", model.ClassBatch)
	q.Push("c", model.ClassBulk)
	q.Push("d", model.ClassBulk)
	q.Push("d", model.ClassBulk) // idempotent: one session, one entry

	counts := q.Snapshot()
	if counts.Normal != 1 || counts.Batch != 1 || counts.Bulk != 2 || counts.Total() != 4 {
		t.Fatalf("counts = %+v, want normal 1 batch 1 bulk 2", counts)
	}

	q.Pop("c")
	if got := q.Snapshot(); got.Bulk != 1 || got.Total() != 3 {
		t.Fatalf("after pop: %+v, want bulk 1 total 3", got)
	}
}

func TestGateServesNormalBeforeBulk(t *testing.T) {
	gate := admission.NewGate(1)
	ctx := context.Background()

	// Take the only slot as batch, so both waiters queue behind it.
	if err := gate.Acquire(ctx, model.ClassBatch); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	order := make(chan model.Class, 2)
	go func() {
		if err := gate.Acquire(ctx, model.ClassBulk); err == nil {
			order <- model.ClassBulk
		}
	}()
	// Bulk is already queued; normal arrives second and must still go first.
	for gate.Waiting() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	go func() {
		if err := gate.Acquire(ctx, model.ClassNormal); err == nil {
			order <- model.ClassNormal
		}
	}()
	for gate.Waiting() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := gate.Waiting(); got != 2 {
		t.Fatalf("waiting = %d, want 2", got)
	}
	gate.Release()

	select {
	case first := <-order:
		if first != model.ClassNormal {
			t.Fatalf("first admitted was %q, want normal before bulk", first)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no waiter was admitted")
	}
}

func TestGateWithoutLimitAdmitsEverything(t *testing.T) {
	gate := admission.NewGate(0)
	for i := 0; i < 50; i++ {
		if err := gate.Acquire(context.Background(), model.ClassBulk); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}
	if got := gate.InUse(); got != 50 {
		t.Fatalf("in use = %d, want 50 with no limit", got)
	}
}

func TestGateHonoursCancellation(t *testing.T) {
	gate := admission.NewGate(1)
	if err := gate.Acquire(context.Background(), model.ClassNormal); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := gate.Acquire(ctx, model.ClassBulk); err == nil {
		t.Fatalf("a cancelled wait was admitted")
	}
	if got := gate.Waiting(); got != 0 {
		t.Fatalf("waiting = %d after cancellation, want 0", got)
	}
}
