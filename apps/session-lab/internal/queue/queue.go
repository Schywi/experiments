// Package queue decides which waiting session gets the next browser slot.
//
// It is the queue experiment's whole subject, kept apart from the runner: no
// Kubernetes client, no runner, no cluster. A trace goes in, a policy decides the
// order, and the simulator reports the wait each class paid. The one thing the
// runner shares is the shape of a request, which is declared in
// internal/runtime/session and adapted here, so nothing in this package is
// imported by session-lab.
package queue

import (
	"fmt"
	"sort"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// Request is one session asking for a browser slot.
//
// Times are nanoseconds relative to the start of the run. Hold is the truth a
// trace carries; EstHold is what the platform would know when the session
// arrives, and zero means nobody predicted anything. Policies that use Hold are
// oracles and are labelled as such wherever a result is printed.
type Request struct {
	ID       int64
	ClientID int64
	Class    model.Class
	Arrival  int64
	Hold     int64
	EstHold  int64
	Bucket   string

	// Start is when the simulator granted the lease; zero means never. Waited is
	// Start-Arrival, filled in by the engine rather than by the policy.
	Start  int64
	Waited int64
}

// Policy orders waiting requests. An algorithm is one implementation of it, and
// the simulator and the live gate both drive the same interface.
//
// Peek and Take are separate so that a caller can ask who is next and decide
// whether it may proceed, without disturbing the order when it may not. Both are
// called under the caller's lock.
type Policy interface {
	// Name is the value --queue takes for this policy.
	Name() string
	// Offer records a request that is now waiting.
	Offer(r *Request, now int64)
	// Peek returns the request that must be served next, without removing it, or
	// nil when nothing is waiting.
	Peek(now int64) *Request
	// Take removes r, which must be the request Peek returned.
	Take(r *Request)
	// Released records that r gave its lease back. Policies keyed on attained
	// service keep their ledger here; every other policy ignores it.
	Released(r *Request, now int64)
	// Len is how many requests are waiting.
	Len() int
}

// Options configures the policies that take a parameter.
type Options struct {
	// AgingNS is the waiting time that buys one class rank. Zero means the
	// policy declines to construct rather than silently never aging.
	AgingNS int64
	// Weights are the share of permits each class receives, keyed by class. A
	// class with no weight gets the default of one.
	Weights map[model.Class]int
	// DemoteAfterNS is how much service a client may consume before 2LPS drops
	// it to the lowest class.
	DemoteAfterNS int64
}

// Kinds are the policy names --queue accepts, in the order the comparison note
// prints them.
var Kinds = []string{"fifo", "prio", "prio-aging", "drr", "twolps", "oracle-est", "oracle-sjf"}

// New builds a policy by name.
//
// The two oracle policies use the trace's true hold, so no live run can supply
// them: they exist to bound what ordering is worth, not to be deployed.
func New(kind string, opts Options) (Policy, error) {
	switch kind {
	case "fifo":
		return NewFIFO(), nil
	case "prio":
		return NewPriority(), nil
	case "prio-aging":
		if opts.AgingNS <= 0 {
			return nil, fmt.Errorf("prio-aging needs a positive aging step")
		}
		return NewAging(opts.AgingNS), nil
	case "drr":
		return NewDRR(opts.Weights), nil
	case "twolps":
		if opts.DemoteAfterNS <= 0 {
			return nil, fmt.Errorf("twolps needs a positive service budget")
		}
		return NewTwoLPS(opts.DemoteAfterNS), nil
	case "oracle-est":
		return NewOracle(false), nil
	case "oracle-sjf":
		return NewOracle(true), nil
	default:
		return nil, fmt.Errorf("unknown queue %q: want one of %v", kind, Kinds)
	}
}

// waiting is the shared bookkeeping every simple policy uses: a slice kept in
// arrival order with a stable tie-break.
type waiting struct {
	items []*Request
}

func (w *waiting) add(r *Request) { w.items = append(w.items, r) }

func (w *waiting) len() int { return len(w.items) }

func (w *waiting) remove(r *Request) {
	for i, item := range w.items {
		if item == r {
			w.items = append(w.items[:i], w.items[i+1:]...)
			return
		}
	}
}

// pick returns the item with the lowest score, breaking ties by arrival and then
// by id so a rerun of the same trace decides identically.
func (w *waiting) pick(score func(*Request, int64) int64, now int64) *Request {
	if len(w.items) == 0 {
		return nil
	}
	best := w.items[0]
	bestScore := score(best, now)
	for _, item := range w.items[1:] {
		s := score(item, now)
		if s < bestScore || (s == bestScore && earlier(item, best)) {
			best, bestScore = item, s
		}
	}
	return best
}

// earlier reports whether a must be served before b when their scores tie.
func earlier(a, b *Request) bool {
	if a.Arrival != b.Arrival {
		return a.Arrival < b.Arrival
	}
	return a.ID < b.ID
}

// rankOf is the class rank as a score component. Lower is served first, which is
// model.Class.Rank's convention.
func rankOf(r *Request) int64 { return int64(r.Class.Rank()) }

// classOrder is every class, best rank first, for iteration in reports.
func classOrder() []model.Class {
	classes := []model.Class{model.ClassNormal, model.ClassBatch, model.ClassBulk}
	sort.SliceStable(classes, func(i, j int) bool { return classes[i].Rank() < classes[j].Rank() })
	return classes
}
