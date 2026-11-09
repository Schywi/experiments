package queue

import (
	"testing"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

func req(id int64, class model.Class, arrival int64) *Request {
	return &Request{ID: id, Class: class, Arrival: arrival, Hold: int64(arrival) + 1}
}

// drain pulls everything a policy will serve, in order.
func drain(p Policy, now int64) []int64 {
	var order []int64
	for {
		r := p.Peek(now)
		if r == nil {
			return order
		}
		p.Take(r)
		order = append(order, r.ID)
	}
}

func wantOrder(t *testing.T, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("served %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("served %v, want %v", got, want)
		}
	}
}

func TestFIFOServesArrivalOrder(t *testing.T) {
	p := NewFIFO()
	// Offered out of order on purpose: the policy must be the thing that sorts.
	p.Offer(req(3, model.ClassNormal, 30), 0)
	p.Offer(req(1, model.ClassBulk, 10), 0)
	p.Offer(req(2, model.ClassNormal, 20), 0)
	wantOrder(t, drain(p, 0), []int64{1, 2, 3})
}

func TestPriorityPutsRankBeforeArrival(t *testing.T) {
	p := NewPriority()
	p.Offer(req(1, model.ClassBulk, 10), 0)
	p.Offer(req(2, model.ClassNormal, 20), 0)
	p.Offer(req(3, model.ClassBatch, 30), 0)
	// rank then arrival, so the normal one goes first even though it arrived last.
	wantOrder(t, drain(p, 0), []int64{2, 3, 1})
}

func TestPriorityHasNoAgingSoBulkKeepsWaiting(t *testing.T) {
	p := NewPriority()
	now := int64(0)
	nextID := int64(100)
	// A bulk request arrives and then sits while normal traffic keeps coming.
	p.Offer(req(1, model.ClassBulk, 0), now)
	served := 0
	for i := 0; i < 20; i++ {
		now += 1_000_000_000
		p.Offer(req(nextID, model.ClassNormal, now), now)
		nextID++
		if r := p.Peek(now); r != nil && r.ID != 1 {
			p.Take(r)
			served++
		}
	}
	if r := p.Peek(now); r == nil || r.ID != 1 {
		t.Fatalf("bulk request was served while normal traffic was still arriving")
	}
	if served != 20 {
		t.Fatalf("served %d normal requests, want 20", served)
	}
}

func TestAgingLetsTheOldestOutrankAFreshNormalRequest(t *testing.T) {
	const step = int64(30_000_000_000) // 30 s buys one rank
	p := NewAging(step)
	p.Offer(req(1, model.ClassBulk, 0), 0)
	// It has waited two steps by now, which is exactly the rank gap from bulk
	// to normal; a normal request arriving at this instant must lose.
	now := 2 * step
	p.Offer(req(2, model.ClassNormal, now), now)
	if r := p.Peek(now); r == nil || r.ID != 1 {
		t.Fatalf("aged bulk request did not overtake a fresh normal one: %+v", r)
	}
}

func TestAgingStillOrdersWithinAClassByArrival(t *testing.T) {
	p := NewAging(int64(30_000_000_000))
	p.Offer(req(2, model.ClassBulk, 10), 0)
	p.Offer(req(1, model.ClassBulk, 20), 0)
	wantOrder(t, drain(p, 0), []int64{2, 1})
}

func TestDRRSplitFollowsTheWeights(t *testing.T) {
	p := NewDRR(map[model.Class]int{
		model.ClassNormal: 4,
		model.ClassBatch:  2,
		model.ClassBulk:   1,
	})
	for i := int64(0); i < 20; i++ {
		p.Offer(&Request{ID: i*3 + 0, Class: model.ClassNormal, Arrival: i, Hold: 1}, 0)
		p.Offer(&Request{ID: i*3 + 1, Class: model.ClassBatch, Arrival: i, Hold: 1}, 0)
		p.Offer(&Request{ID: i*3 + 2, Class: model.ClassBulk, Arrival: i, Hold: 1}, 0)
	}
	counts := map[model.Class]int{}
	for i := 0; i < 7; i++ {
		r := p.Peek(0)
		if r == nil {
			t.Fatalf("policy ran dry after %d grants", i)
		}
		counts[r.Class]++
		p.Take(r)
	}
	want := map[model.Class]int{model.ClassNormal: 4, model.ClassBatch: 2, model.ClassBulk: 1}
	for class, n := range want {
		if counts[class] != n {
			t.Fatalf("7 grants split %v, want %v", counts, want)
		}
	}
}

func TestTwoLPSDemotesAClientThatSpentItsBudget(t *testing.T) {
	const budget = int64(30_000_000_000)
	p := NewTwoLPS(budget)
	// One client, two leases. The first it keeps for longer than the budget.
	first := &Request{ID: 1, ClientID: 7, Class: model.ClassNormal, Arrival: 0, Hold: budget * 2}
	p.Offer(first, 0)
	got := p.Peek(0)
	if got == nil || got.ID != 1 {
		t.Fatalf("the only waiting request was not served")
	}
	p.Take(got)
	// Start is zero, which is where a lease granted at the beginning of a run
	// begins: the service charged is the whole wait since.
	p.Released(got, budget*2)

	// Its second lease arrives with a brand-new normal client beside it.
	second := &Request{ID: 2, ClientID: 7, Class: model.ClassNormal, Arrival: budget * 2, Hold: 1}
	fresh := &Request{ID: 3, ClientID: 8, Class: model.ClassNormal, Arrival: budget*2 + 1, Hold: 1}
	p.Offer(second, budget*2)
	p.Offer(fresh, budget*2)
	if r := p.Peek(budget * 2); r == nil || r.ID != 3 {
		t.Fatalf("a client over its budget was served ahead of a fresh one: %+v", r)
	}
}

func TestTwoLPSLeavesAOneLeaseTraceAlone(t *testing.T) {
	p := NewTwoLPS(int64(30_000_000_000))
	p.Offer(&Request{ID: 1, Class: model.ClassBulk, Arrival: 0, Hold: 1}, 0)
	p.Offer(&Request{ID: 2, Class: model.ClassNormal, Arrival: 1, Hold: 1}, 0)
	// Different anonymous clients (ClientID 0 is shared, but nothing has been
	// released), so this is rank order: normal first.
	wantOrder(t, drain(p, 0), []int64{2, 1})
}

func TestOracleServesShortestHoldFirst(t *testing.T) {
	p := NewOracle(true)
	p.Offer(&Request{ID: 1, Class: model.ClassNormal, Arrival: 0, Hold: 300_000_000_000}, 0)
	p.Offer(&Request{ID: 2, Class: model.ClassNormal, Arrival: 0, Hold: 30_000_000_000}, 0)
	p.Offer(&Request{ID: 3, Class: model.ClassNormal, Arrival: 0, Hold: 120_000_000_000}, 0)
	wantOrder(t, drain(p, 0), []int64{2, 3, 1})
}

func TestOracleOnEstimatesSortsUnknownLast(t *testing.T) {
	p := NewOracle(false)
	p.Offer(&Request{ID: 1, Class: model.ClassNormal, Arrival: 0, Hold: 1, EstHold: 0}, 0)
	p.Offer(&Request{ID: 2, Class: model.ClassNormal, Arrival: 0, Hold: 1, EstHold: 5}, 0)
	wantOrder(t, drain(p, 0), []int64{2, 1})
}

func TestNewRejectsIncompletePolicies(t *testing.T) {
	for _, kind := range []string{"prio-aging", "twolps"} {
		if _, err := New(kind, Options{}); err == nil {
			t.Fatalf("%s built without its parameter", kind)
		}
	}
	if _, err := New("nope", Options{}); err == nil {
		t.Fatalf("an unknown policy name was accepted")
	}
	for _, kind := range Kinds {
		opts := Options{AgingNS: 1, DemoteAfterNS: 1}
		if _, err := New(kind, opts); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}
