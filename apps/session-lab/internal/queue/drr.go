package queue

import (
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// DRR shares permits across classes by weight instead of by rank, so "the fast
// class gets eight permits for every one the bulk class gets" becomes a setting
// rather than whatever strict priority happens to produce.
//
// The mechanism is smooth weighted round robin: every class holds a credit, each
// grant adds its weight to every class and subtracts the total from the class
// that served, and the next grant goes to the class with the highest
// credit+weight. Within a class the order is arrival. Peek is pure; Take applies
// the credit change, so asking who is next never disturbs the rotation.
type DRR struct {
	waiting
	weights map[model.Class]int64
	credit  map[model.Class]int64
	// total is the sum of the weights, subtracted from whichever class serves.
	total int64
}

// NewDRR builds a weighted policy. A class without a weight counts as one.
func NewDRR(weights map[model.Class]int) *DRR {
	w := map[model.Class]int64{}
	total := int64(0)
	for _, class := range classOrder() {
		weight := int64(1)
		if given, ok := weights[class]; ok && given > 0 {
			weight = int64(given)
		}
		w[class] = weight
		total += weight
	}
	d := &DRR{weights: w, credit: map[model.Class]int64{}, total: total}
	return d
}

// Name implements Policy.
func (d *DRR) Name() string { return "drr" }

// Offer implements Policy.
func (d *DRR) Offer(r *Request, now int64) { d.add(r) }

// Peek implements Policy.
func (d *DRR) Peek(now int64) *Request {
	best := (*Request)(nil)
	bestCredit := int64(0)
	for _, class := range classOrder() {
		candidate := d.oldestIn(class)
		if candidate == nil {
			continue
		}
		credit := d.credit[class] + d.weights[class]
		if best == nil || credit > bestCredit || (credit == bestCredit && earlier(candidate, best)) {
			best, bestCredit = candidate, credit
		}
	}
	return best
}

// Take implements Policy.
func (d *DRR) Take(r *Request) {
	d.remove(r)
	for _, class := range classOrder() {
		d.credit[class] += d.weights[class]
	}
	d.credit[r.Class] -= d.total
}

// Released implements Policy.
func (d *DRR) Released(r *Request, now int64) {}

// Len implements Policy.
func (d *DRR) Len() int { return d.len() }

// oldestIn returns the earliest arrival of a class, or nil.
func (d *DRR) oldestIn(class model.Class) *Request {
	var best *Request
	for _, r := range d.items {
		if r.Class != class {
			continue
		}
		if best == nil || earlier(r, best) {
			best = r
		}
	}
	return best
}
