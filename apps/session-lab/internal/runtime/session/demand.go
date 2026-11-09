package session

import (
	"context"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// Demand is the runner's live queue, in the form an autoscaler can size a pool
// from. Claims cannot carry it: a session waiting inside the admission gate or
// the slot pool has no claim at all, and a claim bound to a booting warm-pool
// sandbox is bound rather than pending. See research/session-lifecycle-elasticity/27.
type Demand struct {
	RunID string `json:"run_id"`
	// Live is every session that has arrived and not settled, so
	// ceil(Live/ContextsPerBrowser) is the fleet that would serve this instant
	// with no queue at all.
	Live int `json:"live"`
	// Waiting is how many of those have no lease yet, at the gate or in the pool.
	Waiting int `json:"waiting"`
	// GateInUse and GateWaiting separate the two waits a session can be in.
	GateInUse int   `json:"gate_in_use"`
	GateWait  int   `json:"gate_waiting"`
	Contexts  int   `json:"contexts_per_browser"`
	MaxPods   int   `json:"max_pods,omitempty"`
	UpdatedNS int64 `json:"updated_ns"`
	// ClassWaiting is the wait per class, which is what a two-class queue needs
	// to report separately.
	ClassWaiting map[string]int `json:"class_waiting,omitempty"`
}

// DemandSink writes a Demand somewhere a controller can read it. The runner only
// calls it; where it lands is the command's business.
type DemandSink interface {
	PublishDemand(ctx context.Context, demand Demand) error
}

// DemandInterval is how often the runner publishes its depth. One second is far
// below the cost of the action it drives: the autoscaler reconciles every 5 s and
// a pod takes ~40 s to boot.
const DemandInterval = time.Second

// snapshotDemand reads the counters the dispatcher already keeps.
func (r *Runner) snapshotDemand() Demand {
	r.mu.Lock()
	demand := Demand{
		RunID:   r.cfg.RunID,
		Live:    int(r.inFlightN),
		Waiting: int(r.waitingN),
		Contexts: func() int {
			if r.cfg.ContextsPerBrowser < 1 {
				return 1
			}
			return r.cfg.ContextsPerBrowser
		}(),
		UpdatedNS: r.now().UnixNano(),
	}
	r.mu.Unlock()
	if r.gate != nil {
		demand.GateInUse = r.gate.InUse()
		demand.GateWait = r.gate.Waiting()
	}
	if r.queue != nil {
		counts := r.queue.Snapshot()
		demand.ClassWaiting = map[string]int{
			string(model.ClassNormal): counts.Normal,
			string(model.ClassBatch):  counts.Batch,
			string(model.ClassBulk):   counts.Bulk,
		}
	}
	return demand
}

// publishDemand runs until the context ends. A publish failure is logged and the
// loop continues: a run must not die because a controller could not be told
// anything.
func (r *Runner) publishDemand(ctx context.Context) {
	ticker := time.NewTicker(DemandInterval)
	defer ticker.Stop()
	publish := func() {
		if err := r.demand.PublishDemand(ctx, r.snapshotDemand()); err != nil && ctx.Err() == nil {
			r.log.Printf("demand: %v", err)
		}
	}
	publish()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			publish()
		}
	}
}
