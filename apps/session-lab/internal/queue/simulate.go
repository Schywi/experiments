package queue

import (
	"sort"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// Config is the fleet the simulation runs against. Every field corresponds to
// something the runner or the cluster actually has, so a result can be traced
// back to a setting rather than to a modelling choice.
type Config struct {
	// Policy decides the order. Required.
	Policy Policy

	// Slots is a fixed number of concurrent leases with no boot cost: the shape
	// to use when the question is only about ordering. When it is positive,
	// SlotsPerPod, MaxPods and Boot are ignored.
	Slots int

	// The elastic shape: a pod carries SlotsPerPod leases, up to MaxPods pods,
	// and each new pod costs Boot nanoseconds before its leases exist.
	SlotsPerPod int
	MaxPods     int
	Boot        int64
	// Provisioning is how many pod creations may be in flight at once. One is
	// what slots.Pool does today, and it is the reason the fleet grows in single
	// steps while thousands of sessions wait.
	Provisioning int

	// DecisionLatency is charged against the clock on every Offer and Peek, so a
	// shared queue's round trip can be priced before anything is deployed.
	DecisionLatency int64
}

// Result is what one policy paid on one trace.
type Result struct {
	Policy    string
	Requests  int
	Started   int
	Makespan  int64
	BusyNS    int64
	Waits     map[model.Class][]int64
	Starved   map[model.Class]int
	Decisions int
	// DecisionNS is the simulated cost of making those decisions.
	DecisionNS int64
	// PeakDepth is the deepest the queue got, and PeakPods the largest fleet.
	PeakDepth int
	PeakPods  int
	// SlotNS is the lease-time that existed over the run, integrated as the fleet
	// grew rather than taken at its final size.
	SlotNS int64
	// Utilisation is busy slot-time over the slot-time that existed.
	Utilisation float64
	// Fairness is Jain's index over each class's mean wait: 1 means every class
	// waited the same on average, and a lower number means the policy moved wait
	// from one class to another. It is the number that makes the transfer a
	// priority policy performs visible instead of hiding it in an average.
	Fairness float64
}

// Simulate runs one trace through one policy.
//
// It is a discrete-event loop over three kinds of moment: a session arrives, a
// lease is returned, a pod becomes usable. Nothing is concurrent, so a result is
// reproducible byte for byte from the same trace and the same Config.
func Simulate(trace []Request, cfg Config) Result {
	requests := make([]Request, len(trace))
	copy(requests, trace)
	sort.SliceStable(requests, func(i, j int) bool {
		if requests[i].Arrival != requests[j].Arrival {
			return requests[i].Arrival < requests[j].Arrival
		}
		return requests[i].ID < requests[j].ID
	})

	// pointers into requests, so a policy sees the same identity a report does.
	ptrs := make([]*Request, len(requests))
	for i := range requests {
		ptrs[i] = &requests[i]
	}

	res := Result{
		Policy:   cfg.Policy.Name(),
		Requests: len(requests),
		Waits:    map[model.Class][]int64{},
		Starved:  map[model.Class]int{},
	}

	slotsPerPod := cfg.SlotsPerPod
	if slotsPerPod < 1 {
		slotsPerPod = 1
	}
	fixed := cfg.Slots > 0

	free := 0
	capacity := 0
	pods := 0
	provisioning := 0
	var readyAt []int64 // one entry per pod being created
	if fixed {
		free = cfg.Slots
		capacity = cfg.Slots
	}

	type lease struct {
		r   *Request
		end int64
	}
	var running []lease

	next := 0
	now := int64(0)
	if len(ptrs) > 0 {
		now = ptrs[0].Arrival
	}

	// decide prices one policy call against the clock and the tally.
	decide := func() {
		res.Decisions++
		if cfg.DecisionLatency > 0 {
			now += cfg.DecisionLatency
			res.DecisionNS += cfg.DecisionLatency
		}
	}

	admit := func() {
		for next < len(ptrs) && ptrs[next].Arrival <= now {
			cfg.Policy.Offer(ptrs[next], now)
			next++
			decide()
			if depth := cfg.Policy.Len(); depth > res.PeakDepth {
				res.PeakDepth = depth
			}
		}
	}

	grant := func() {
		for free > 0 {
			r := cfg.Policy.Peek(now)
			decide()
			if r == nil {
				return
			}
			cfg.Policy.Take(r)
			r.Start = now
			r.Waited = now - r.Arrival
			free--
			running = append(running, lease{r: r, end: now + r.Hold})
			res.Started++
			res.Waits[r.Class] = append(res.Waits[r.Class], r.Waited)
			res.BusyNS += r.Hold
		}
	}

	grow := func() {
		if fixed {
			return
		}
		if free > 0 || cfg.Policy.Len() == 0 {
			return
		}
		limit := cfg.Provisioning
		if limit < 1 {
			limit = 1 << 30
		}
		if provisioning >= limit {
			return
		}
		if cfg.MaxPods > 0 && pods+provisioning >= cfg.MaxPods {
			return
		}
		provisioning++
		readyAt = append(readyAt, now+cfg.Boot)
	}

	// The loop ends when every session has arrived, been served and given its
	// lease back. A trace that cannot be finished (no capacity and no growth)
	// would hang here, so the guard below reports it instead.
	for {
		admit()
		grant()
		grow()

		if next >= len(ptrs) && cfg.Policy.Len() == 0 && len(running) == 0 && len(readyAt) == 0 {
			break
		}

		// The next moment: an arrival, a lease ending, or a pod becoming usable.
		found := false
		candidate := int64(0)
		consider := func(at int64) {
			if !found || at < candidate {
				candidate, found = at, true
			}
		}
		if next < len(ptrs) {
			consider(ptrs[next].Arrival)
		}
		for _, l := range running {
			consider(l.end)
		}
		for _, at := range readyAt {
			consider(at)
		}
		if !found {
			break // nothing can ever happen: the trace outran the fleet
		}
		if candidate < now {
			candidate = now
		}
		res.SlotNS += int64(capacity) * (candidate - now)
		now = candidate

		// A lease ended: return it to the pool and tell the policy.
		if len(running) > 0 {
			kept := running[:0]
			for _, l := range running {
				if l.end <= now {
					free++
					cfg.Policy.Released(l.r, l.end)
					decide()
					continue
				}
				kept = append(kept, l)
			}
			running = kept
		}

		// A pod finished booting: its leases exist from now on.
		if len(readyAt) > 0 {
			kept := readyAt[:0]
			for _, at := range readyAt {
				if at <= now {
					provisioning--
					pods++
					free += slotsPerPod
					capacity += slotsPerPod
					if pods > res.PeakPods {
						res.PeakPods = pods
					}
					continue
				}
				kept = append(kept, at)
			}
			readyAt = kept
		}

		if cfg.MaxPods > 0 && pods >= cfg.MaxPods && free == 0 && cfg.Policy.Len() > 0 && next >= len(ptrs) {
			// Full fleet, work waiting, nothing to return: this trace cannot
			// finish. Stop rather than spin; Started < Requests says so.
			break
		}
	}

	if fixed && res.PeakPods == 0 {
		res.PeakPods = cfg.Slots / slotsPerPod
		if res.PeakPods < 1 {
			res.PeakPods = 1
		}
	}

	if len(requests) > 0 {
		end := int64(0)
		for i := range requests {
			if at := requests[i].Start + requests[i].Hold; at > end {
				end = at
			}
		}
		res.Makespan = end - now0(requests)
	}
	if res.SlotNS > 0 {
		res.Utilisation = float64(res.BusyNS) / float64(res.SlotNS)
	}
	res.Fairness = fairness(res)
	markStarved(&res)
	return res
}

// now0 is the earliest arrival, the origin a makespan is measured from.
func now0(requests []Request) int64 {
	base := int64(0)
	for _, r := range requests {
		if base == 0 || r.Arrival < base {
			base = r.Arrival
		}
	}
	return base
}

// fairness is Jain's index over the classes' mean waits.
func fairness(res Result) float64 {
	var served []float64
	for _, class := range classOrder() {
		waits := res.Waits[class]
		if len(waits) == 0 {
			continue
		}
		total := float64(0)
		for _, wait := range waits {
			total += float64(wait)
		}
		served = append(served, total/float64(len(waits)))
	}
	n := float64(len(served))
	if n < 2 {
		// One class cannot be treated unequally, so there is nothing to measure.
		return 1
	}
	if n == 0 {
		return 0
	}
	sum, squares := 0.0, 0.0
	for _, v := range served {
		sum += v
		squares += v * v
	}
	if squares == 0 {
		return 1
	}
	return sum * sum / (n * squares)
}

// starveFactor is the multiple of a class's own mean wait above which a session
// counts as starved. It is the definition HANDOFF.md §4 asks the benchmark to
// report, stated once here.
const starveFactor = 5.0

// markStarved fills Starved per class.
func markStarved(res *Result) {
	if res.Starved == nil {
		res.Starved = map[model.Class]int{}
	}
	for class, waits := range res.Waits {
		if len(waits) == 0 {
			continue
		}
		total := float64(0)
		for _, wait := range waits {
			total += float64(wait)
		}
		mean := total / float64(len(waits))
		count := 0
		for _, wait := range waits {
			if float64(wait) > starveFactor*mean {
				count++
			}
		}
		res.Starved[class] = count
	}
}
