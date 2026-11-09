package queue

import (
	"context"
	"sync"
	"time"
)

// Live is a Policy as a gate a running process can call: at most limit sessions
// hold an admission permit at once, and the policy decides which waiter gets the
// next one.
//
// It exists because the runner's ordering and its slot pool are two different
// things. admission.Gate orders by class and then lets a waking waiter race for
// the pool; a Live gate grants the permit to exactly one waiter, chosen by the
// policy, so what a run measures is the policy rather than whichever goroutine
// the scheduler ran first.
//
// The bound matters as much as the order: the experiment's arms run with one
// permit, which is what makes the slot pool's ordering a function of the gate's
// ordering.
type Live struct {
	mu     sync.Mutex
	policy Policy
	limit  int
	inUse  int

	// start is the origin every request's arrival is measured from.
	start time.Time

	pending []*liveLease
	// running maps a session to the lease it was granted, so Release can hand
	// the policy back the same request identity it offered.
	running map[int64]*liveLease

	decisions int64
}

// liveLease is one waiting request and the channel its grant arrives on.
type liveLease struct {
	req     Request
	ready   chan struct{}
	granted bool
}

// NewLive builds a gate. A limit below one is treated as one, so a caller cannot
// accidentally unbound the gate by passing zero.
func NewLive(p Policy, limit int) *Live {
	if limit < 1 {
		limit = 1
	}
	return &Live{policy: p, limit: limit, start: time.Now(), running: map[int64]*liveLease{}}
}

// Now is the run's monotonic clock in nanoseconds: what a request's arrival and
// service are measured against.
func (l *Live) Now() int64 { return time.Since(l.start).Nanoseconds() }

// Acquire blocks until this request may proceed, or the context ends.
//
// The request's Arrival is stamped here, at the moment it reached the gate, which
// is the same instant the run measures its own admission wait from. A caller that
// already knows a better arrival may set it.
func (l *Live) Acquire(ctx context.Context, req Request) error {
	l.mu.Lock()
	now := l.Now()
	if req.Arrival == 0 {
		req.Arrival = now
	}
	w := &liveLease{req: req, ready: make(chan struct{})}
	l.policy.Offer(&w.req, now)
	l.pending = append(l.pending, w)
	l.grantLocked(now)
	l.mu.Unlock()

	select {
	case <-w.ready:
		return nil
	case <-ctx.Done():
		l.mu.Lock()
		defer l.mu.Unlock()
		if w.granted {
			// The permit arrived at the same moment the context ended: give it
			// back rather than leaking it.
			l.releaseLocked(w)
			return ctx.Err()
		}
		l.abandonLocked(w)
		return ctx.Err()
	}
}

// Release returns the permit this request took.
func (l *Live) Release(req Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w, ok := l.running[req.ID]; ok {
		l.releaseLocked(w)
	}
	l.grantLocked(l.Now())
}

// Decisions is how many policy calls the gate has made, for a report that wants
// to price the ordering rather than the fleet.
func (l *Live) Decisions() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.decisions
}

// Depth is how many requests are waiting for a permit.
func (l *Live) Depth() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.pending)
}

// grantLocked hands permits to the policy's choices until the bound is reached.
func (l *Live) grantLocked(now int64) {
	for l.inUse < l.limit {
		next := l.policy.Peek(now)
		l.decisions++
		if next == nil {
			return
		}
		w := l.ownerOfLocked(next)
		if w == nil {
			// The policy returned something this gate does not know: take it out
			// so the gate cannot spin, and let the waiting side time out.
			l.policy.Take(next)
			return
		}
		l.policy.Take(next)
		w.granted = true
		w.req.Start = now
		l.inUse++
		l.running[w.req.ID] = w
		l.removeLocked(w)
		close(w.ready)
	}
}

// releaseLocked ends one grant and charges the policy for the service it got.
func (l *Live) releaseLocked(w *liveLease) {
	l.policy.Released(&w.req, l.Now())
	delete(l.running, w.req.ID)
	if l.inUse > 0 {
		l.inUse--
	}
	w.granted = false
}

// abandonLocked drops a waiter that gave up before being granted.
func (l *Live) abandonLocked(w *liveLease) {
	l.policy.Take(&w.req)
	l.removeLocked(w)
}

func (l *Live) removeLocked(w *liveLease) {
	for i, item := range l.pending {
		if item == w {
			l.pending = append(l.pending[:i], l.pending[i+1:]...)
			return
		}
	}
}

// ownerOfLocked finds the waiting lease a policy's request belongs to.
func (l *Live) ownerOfLocked(r *Request) *liveLease {
	for _, w := range l.pending {
		if &w.req == r {
			return w
		}
	}
	return nil
}
