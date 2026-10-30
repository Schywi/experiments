// Package admission decides the order sessions are allowed to reach the cluster.
//
// It is the demand-side counterpart to the capacity plane: sessions queue here,
// by class, and only a bounded number are in the act of acquiring an instance at
// once. Nothing is rejected — a session that waits here is still a session, and
// the queue never closes.
package admission

import (
	"context"
	"sync"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// Counts is how many sessions are queued in each class.
type Counts struct {
	Normal int
	Batch  int
	Bulk   int
}

// Total is every queued session.
func (c Counts) Total() int { return c.Normal + c.Batch + c.Bulk }

// Queue records which sessions are waiting and in what class. The Gate is what
// actually orders them; the queue is the record the report and the metrics read.
type Queue struct {
	mu     sync.Mutex
	byID   map[string]model.Class
	counts Counts
}

// NewQueue builds an empty queue.
func NewQueue() *Queue {
	return &Queue{byID: make(map[string]model.Class)}
}

// Push records a waiting session.
func (q *Queue) Push(id string, class model.Class) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, busy := q.byID[id]; busy {
		return
	}
	q.byID[id] = class
	switch class {
	case model.ClassBatch:
		q.counts.Batch++
	case model.ClassBulk:
		q.counts.Bulk++
	default:
		q.counts.Normal++
	}
}

// Pop forgets a session that is no longer waiting.
func (q *Queue) Pop(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	class, ok := q.byID[id]
	if !ok {
		return
	}
	delete(q.byID, id)
	switch class {
	case model.ClassBatch:
		q.counts.Batch--
	case model.ClassBulk:
		q.counts.Bulk--
	default:
		q.counts.Normal--
	}
}

// Snapshot returns the counts per class.
func (q *Queue) Snapshot() Counts {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.counts
}

// Gate bounds how many sessions are acquiring an instance at the same time and
// serves waiters in class order, then in arrival order. A limit of zero means no
// bound: every session acquires immediately, which is the default.
type Gate struct {
	mu      sync.Mutex
	limit   int
	inUse   int
	waiters []*waiter
	seq     uint64
}

type waiter struct {
	class model.Class
	seq   uint64
	ready chan struct{}
}

// NewGate builds a gate. limit <= 0 disables it.
func NewGate(limit int) *Gate {
	return &Gate{limit: limit}
}

// Acquire blocks until this class may proceed. Ordering rules: a session of a
// better class never waits behind a worse one, and sessions of the same class are
// served in arrival order.
func (g *Gate) Acquire(ctx context.Context, class model.Class) error {
	g.mu.Lock()
	if g.limit <= 0 || (g.inUse < g.limit && !g.betterWaiting(class)) {
		g.inUse++
		g.mu.Unlock()
		return nil
	}
	w := &waiter{class: class, seq: g.seq, ready: make(chan struct{})}
	g.seq++
	g.enqueue(w)
	g.mu.Unlock()

	select {
	case <-ctx.Done():
		g.mu.Lock()
		g.remove(w)
		g.mu.Unlock()
		return ctx.Err()
	case <-w.ready:
		return nil
	}
}

// Release frees one admission slot and wakes the best waiter, if any.
func (g *Gate) Release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inUse > 0 {
		g.inUse--
	}
	if g.limit > 0 && g.inUse >= g.limit {
		return
	}
	if len(g.waiters) == 0 {
		return
	}
	next := g.waiters[0]
	g.waiters = g.waiters[1:]
	g.inUse++
	close(next.ready)
}

// InUse is how many sessions hold an admission slot.
func (g *Gate) InUse() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inUse
}

// Waiting is how many sessions are blocked on the gate.
func (g *Gate) Waiting() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.waiters)
}

// betterWaiting reports whether a waiter outranks class, which would make
// jumping the queue unfair.
func (g *Gate) betterWaiting(class model.Class) bool {
	for _, w := range g.waiters {
		if w.class.Rank() < class.Rank() {
			return true
		}
	}
	return false
}

// enqueue inserts keeping the waiters ordered by class rank, then arrival.
func (g *Gate) enqueue(w *waiter) {
	at := len(g.waiters)
	for i, existing := range g.waiters {
		if w.class.Rank() < existing.class.Rank() {
			at = i
			break
		}
	}
	g.waiters = append(g.waiters, nil)
	copy(g.waiters[at+1:], g.waiters[at:])
	g.waiters[at] = w
}

// remove drops a waiter that gave up.
func (g *Gate) remove(target *waiter) {
	for i, w := range g.waiters {
		if w == target {
			g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
			return
		}
	}
}
