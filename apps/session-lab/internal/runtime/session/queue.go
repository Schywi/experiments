package session

import (
	"context"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// Queue decides which waiting session may proceed, and it is the only seam the
// queue experiment needs in the runner.
//
// When a run supplies one, it replaces the ordering admission.Gate performs and
// nothing else: the slot pool, the boot permit, the claim and the readiness
// watch all keep their behaviour. A nil Queue is the default and leaves every
// existing path byte-identical, which is what makes the control arm in a queue
// A/B the absence of a field rather than a second code path.
//
// The interface is declared here, in the runner, and implemented elsewhere, so
// the queue experiment's own package is never imported by session-lab.
type Queue interface {
	// Acquire blocks until this session may proceed. It must honour ctx.
	Acquire(ctx context.Context, req QueueRequest) error
	// Release returns the permit Acquire took. It is called exactly once per
	// successful Acquire.
	Release(req QueueRequest)
}

// QueueRequest is what a queue is told about a waiting session. It carries what
// the runner knows at admission time and nothing it would have to guess: the
// hold is the plan's, so a policy that treats it as a prediction is reading the
// workload generator. SessionID is the identity a policy accumulates service
// against, and Claim is for log lines.
type QueueRequest struct {
	SessionID int64
	Claim     string
	Bucket    string
	Hold      time.Duration
	Class     model.Class
}

// admitOrder takes an admission permit, through the run's queue when it has one
// and through admission.Gate otherwise. It is named apart from Runner.release,
// which returns an instance to the pool.
func (r *Runner) admitOrder(ctx context.Context, req QueueRequest) error {
	if r.order != nil {
		return r.order.Acquire(ctx, req)
	}
	return r.gate.Acquire(ctx, req.Class)
}

// releaseOrder returns an admission permit to whichever of the two granted it.
func (r *Runner) releaseOrder(req QueueRequest) {
	if r.order != nil {
		r.order.Release(req)
		return
	}
	r.gate.Release()
}
