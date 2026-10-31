// Package schedule turns a run configuration into a session plan.
//
// It has no notion of a maximum: it produces exactly the sessions the operator
// asked for, in the buckets they asked for, and hands them to the runner, which
// starts a goroutine per session unless a worker count was given.
package schedule

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// Planned is one generated session, before it reaches the cluster.
type Planned struct {
	ID     int64
	Bucket string
	Hold   time.Duration
	Offset time.Duration
}

// Plan is the whole generated workload.
type Plan struct {
	Sessions []Planned
	Rate     float64
	Span     time.Duration
}

// Generate builds the plan for cfg. Arrival offsets are relative to run start.
//
// The arrival rate follows offered load: the nominal rate is the one that would
// keep a single instance busy for the whole run (sessions divided by total hold
// time), and --overload-factor multiplies it. A factor of 1 offers exactly one
// instance's worth of work; 4 offers four times that, which is what makes the
// queue, and the platform's response to it, visible.
func Generate(cfg model.RunConfig, seed int64) (Plan, error) {
	if len(cfg.Mix) == 0 {
		return Plan{}, fmt.Errorf("no buckets in the run configuration")
	}

	total := 0
	var totalHold time.Duration
	for _, bucket := range cfg.Mix {
		if bucket.Count < 0 {
			return Plan{}, fmt.Errorf("bucket %s has a negative count", bucket.Label)
		}
		if bucket.Hold <= 0 {
			return Plan{}, fmt.Errorf("bucket %s has a non-positive hold", bucket.Label)
		}
		total += bucket.Count
		totalHold += time.Duration(bucket.Count) * bucket.Hold
	}
	if total == 0 {
		return Plan{}, fmt.Errorf("the plan has no sessions")
	}

	// Interleave the buckets so short and long sessions overlap in time. Blocks
	// of one length would make every hour of the run a different experiment.
	labels := make([]int, 0, total)
	remaining := make([]int, len(cfg.Mix))
	for i, bucket := range cfg.Mix {
		remaining[i] = bucket.Count
	}
	for len(labels) < total {
		for i := range cfg.Mix {
			if remaining[i] == 0 {
				continue
			}
			labels = append(labels, i)
			remaining[i]--
		}
	}

	span := totalHold
	nominal := float64(total) / totalHold.Seconds()
	rate := nominal
	if cfg.OverloadFactor > 0 {
		rate = nominal * cfg.OverloadFactor
	}
	if cfg.Rate > 0 {
		rate = cfg.Rate
	}
	if rate <= 0 || math.IsInf(rate, 0) || math.IsNaN(rate) {
		return Plan{}, fmt.Errorf("arrival rate resolved to %v", rate)
	}

	offsets := make([]time.Duration, total)
	if cfg.FixedArrival {
		for i := 0; i < total; i++ {
			offsets[i] = time.Duration(float64(i) / rate * float64(time.Second))
		}
	} else {
		// Poisson arrivals: exponential interarrival times, mean 1/rate.
		rng := rand.New(rand.NewSource(seed))
		at := 0.0
		for i := 0; i < total; i++ {
			at += rng.ExpFloat64() / rate
			offsets[i] = time.Duration(at * float64(time.Second))
		}
	}
	span = offsets[total-1]

	plan := Plan{Sessions: make([]Planned, 0, total), Rate: rate, Span: span}
	for i, bucketIndex := range labels {
		bucket := cfg.Mix[bucketIndex]
		plan.Sessions = append(plan.Sessions, Planned{
			ID:     int64(i + 1),
			Bucket: bucket.Label,
			Hold:   bucket.Hold,
			Offset: offsets[i],
		})
	}
	return plan, nil
}

// PeakOffered is the concurrency the plan offers at its busiest instant, from
// Little's law on the generated schedule. It is reported next to the measured
// peak so a run says whether the workload ever reached what was asked for.
func (p Plan) PeakOffered() float64 {
	offered := p.Rate * p.MeanHold().Seconds()
	// Little's law describes a stream, not a batch: eighteen sessions at 2/s with
	// 20s holds cannot offer 40 at once, because only eighteen exist.
	if sessions := float64(len(p.Sessions)); offered > sessions {
		return sessions
	}
	return offered
}

// MeanHold is the count-weighted mean hold duration.
func (p Plan) MeanHold() time.Duration {
	var sum time.Duration
	for _, s := range p.Sessions {
		sum += s.Hold
	}
	if len(p.Sessions) == 0 {
		return 0
	}
	return sum / time.Duration(len(p.Sessions))
}
