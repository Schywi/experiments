package queue

import (
	"math"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// FIFO serves whoever arrived first. It is the control arm: it matches what the
// runner does when no queue is set, minus the class ordering admission.Gate adds.
type FIFO struct{ waiting }

// NewFIFO builds an arrival-order policy.
func NewFIFO() *FIFO { return &FIFO{} }

// Name implements Policy.
func (f *FIFO) Name() string { return "fifo" }

// Offer implements Policy.
func (f *FIFO) Offer(r *Request, now int64) { f.add(r) }

// Peek implements Policy.
func (f *FIFO) Peek(now int64) *Request {
	return f.pick(func(r *Request, _ int64) int64 { return r.Arrival }, now)
}

// Take implements Policy.
func (f *FIFO) Take(r *Request) { f.remove(r) }

// Released implements Policy.
func (f *FIFO) Released(r *Request, now int64) {}

// Len implements Policy.
func (f *FIFO) Len() int { return f.len() }

// Priority is admission.Gate's rule as a policy: class rank first, arrival
// second. It has no aging, so a bulk request behind a steady stream of normal
// arrivals waits for as long as that stream lasts.
type Priority struct{ waiting }

// NewPriority builds the static-priority policy.
func NewPriority() *Priority { return &Priority{} }

// Name implements Policy.
func (p *Priority) Name() string { return "prio" }

// Offer implements Policy.
func (p *Priority) Offer(r *Request, now int64) { p.add(r) }

// Peek implements Policy.
func (p *Priority) Peek(now int64) *Request {
	return p.pick(func(r *Request, _ int64) int64 { return rankOf(r) }, now)
}

// Take implements Policy.
func (p *Priority) Take(r *Request) { p.remove(r) }

// Released implements Policy.
func (p *Priority) Released(r *Request, now int64) {}

// Len implements Policy.
func (p *Priority) Len() int { return p.len() }

// Aging is static priority plus a clock: rank is worth agingNS of waiting, so a
// request's effective rank improves as its wait grows and the worst-case wait
// becomes a number instead of a hope.
//
// With rank r at arrival time a and step s, the score is r*s-(now-a). A bulk
// request (rank 2) overtakes a just-arrived normal one (rank 1) after it has
// waited s, and reaches the front of its own class in the order its members
// arrived. The bound this buys: no request waits behind more than rank*s of
// higher-ranked work before its own rank is spent.
type Aging struct {
	waiting
	step int64
}

// NewAging builds the aging policy. step must be positive.
func NewAging(step int64) *Aging { return &Aging{step: step} }

// Name implements Policy.
func (a *Aging) Name() string { return "prio-aging" }

// Offer implements Policy.
func (a *Aging) Offer(r *Request, now int64) { a.add(r) }

// Peek implements Policy.
func (a *Aging) Peek(now int64) *Request {
	return a.pick(func(r *Request, now int64) int64 {
		return rankOf(r)*a.step - (now - r.Arrival)
	}, now)
}

// Take implements Policy.
func (a *Aging) Take(r *Request) { a.remove(r) }

// Released implements Policy.
func (a *Aging) Released(r *Request, now int64) {}

// Len implements Policy.
func (a *Aging) Len() int { return a.len() }

// Oracle serves the shortest remaining hold first, which no deployable policy can
// do because the hold is not known when a session arrives. It exists to say what
// ordering is worth at most.
//
// With trueHold it sorts on the trace's real hold (oracle-sjf, the upper bound);
// without it, on the estimate carried at arrival (oracle-est, which is what a
// perfect classifier would achieve). A request with no estimate sorts last.
type Oracle struct {
	waiting
	trueHold bool
}

// NewOracle builds an oracle policy.
func NewOracle(trueHold bool) *Oracle { return &Oracle{trueHold: trueHold} }

// Name implements Policy.
func (o *Oracle) Name() string {
	if o.trueHold {
		return "oracle-sjf"
	}
	return "oracle-est"
}

// Offer implements Policy.
func (o *Oracle) Offer(r *Request, now int64) { o.add(r) }

// Peek implements Policy.
func (o *Oracle) Peek(now int64) *Request {
	return o.pick(func(r *Request, _ int64) int64 {
		size := r.EstHold
		if o.trueHold {
			size = r.Hold
		}
		if size <= 0 {
			return math.MaxInt64
		}
		return size
	}, now)
}

// Take implements Policy.
func (o *Oracle) Take(r *Request) { o.remove(r) }

// Released implements Policy.
func (o *Oracle) Released(r *Request, now int64) {}

// Len implements Policy.
func (o *Oracle) Len() int { return o.len() }

// TwoLPS is the deployable approximation of shortest-first: a client starts in
// its declared class and, once it has actually consumed budget nanoseconds of
// browser time, is dropped behind everything else. Prediction is never needed,
// and a long job misjudged as short is punished by the clock instead of holding
// the front of the queue for its whole life.
//
// Service is accumulated per ClientID, so an agent that returns for a second
// lease carries what it already spent. A client of zero is anonymous and each
// request stands alone, which is what a one-lease-per-session run looks like.
type TwoLPS struct {
	waiting
	budget   int64
	attained map[int64]int64
	demoted  map[int64]bool
}

// NewTwoLPS builds the demoting policy. budget must be positive.
func NewTwoLPS(budget int64) *TwoLPS {
	return &TwoLPS{budget: budget, attained: map[int64]int64{}, demoted: map[int64]bool{}}
}

// Name implements Policy.
func (t *TwoLPS) Name() string { return "twolps" }

// Offer implements Policy.
func (t *TwoLPS) Offer(r *Request, now int64) { t.add(r) }

// Peek implements Policy.
func (t *TwoLPS) Peek(now int64) *Request {
	return t.pick(func(r *Request, _ int64) int64 {
		if t.demoted[r.ClientID] {
			// Behind every declared class, in arrival order among its own kind.
			return int64(model.ClassBulk.Rank()) + 1
		}
		return rankOf(r)
	}, now)
}

// Take implements Policy.
func (t *TwoLPS) Take(r *Request) { t.remove(r) }

// Released implements Policy. It charges the client the service it received and
// demotes it once the budget is spent.
//
// Released is called only for a lease that was granted, so Start is always the
// moment the lease began — including zero, which is a legitimate start for a
// request that arrived and was served at the beginning of a run. Callers that
// have no such guarantee do not call it.
func (t *TwoLPS) Released(r *Request, now int64) {
	served := now - r.Start
	if served <= 0 {
		return
	}
	t.attained[r.ClientID] += served
	if t.attained[r.ClientID] >= t.budget {
		t.demoted[r.ClientID] = true
	}
}

// Len implements Policy.
func (t *TwoLPS) Len() int { return t.len() }

// Attained is how much service a client has consumed so far, for a report.
func (t *TwoLPS) Attained(client int64) int64 { return t.attained[client] }
