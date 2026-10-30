package session

// The shared-browser path: one Chromium serves several sessions, each in its own
// browser context. Capacity is requested only when every slot the pool knows
// about is taken, so the claim count tracks *deficits*, not sessions.

import (
	"context"
	"fmt"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/slots"
)

// acquireInstance is the pool's provider: it is the only place the shared path
// reaches the capacity plane, and it runs only when a session has no slot.
func (r *Runner) acquireInstance(ctx context.Context) (*sandbox.Instance, error) {
	r.mu.Lock()
	r.instanceSeq++
	seq := int64(r.instanceSeq)
	r.mu.Unlock()

	name := claimName(r.cfg.RunID, seq)
	r.log.Printf("capacity: no free slot, acquiring %s", name)

	instance, err := r.ad.Acquire(ctx, r.cfg.Namespace, r.cfg.Pool, name, r.cfg.Class)
	if err != nil {
		return nil, fmt.Errorf("acquire: %w", err)
	}

	waitCtx := ctx
	if r.cfg.SessionDeadline > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, r.cfg.SessionDeadline)
		defer cancel()
	}
	if err := r.ad.WaitReady(waitCtx, instance); err != nil {
		// Never leave a claim behind for a slot that cannot be used.
		_ = r.deleteWithRetry(ctx, instance)
		return nil, fmt.Errorf("ready: %w", err)
	}

	r.mu.Lock()
	r.all[instance.ID] = instance
	r.mu.Unlock()
	r.persistInstance(instance)
	r.log.Printf("capacity: %s ready (%s)", instance.ID, launchType(instance))
	return instance, nil
}

// sessionShared runs one session inside a browser context on a shared Chromium.
func (r *Runner) sessionShared(ctx context.Context, planned schedule.Planned) {
	session := model.Session{
		ID: planned.ID, Bucket: planned.Bucket, Policy: r.cfg.Policy,
		Namespace: r.cfg.Namespace, Template: r.cfg.Template, Pool: r.cfg.Pool,
		Outcome: model.OutcomePending, ArrivalNS: r.start.Add(planned.Offset).UnixNano(),
	}

	// Admission first: the class decides the queue order and whether this
	// session may ask for more capacity at all.
	name := claimName(r.cfg.RunID, planned.ID)
	if r.queue != nil {
		r.queue.Push(name, r.cfg.Class)
	}
	admitStart := r.now()
	if err := r.gate.Acquire(ctx, r.cfg.Class); err != nil {
		if r.queue != nil {
			r.queue.Pop(name)
		}
		r.settle(session.ID, model.OutcomeWaiting, "admission: "+err.Error(), nil)
		return
	}
	if r.queue != nil {
		r.queue.Pop(name)
	}
	session.AdmissionWaitNS = r.now().Sub(admitStart).Nanoseconds()
	defer r.gate.Release()

	lease, err := r.spool.Take(ctx)
	if err != nil {
		outcome := model.OutcomeAcquisitionFailed
		if ctx.Err() != nil {
			outcome = model.OutcomeWaiting
		}
		r.settle(session.ID, outcome, "slot: "+err.Error(), nil)
		return
	}
	session.Instance = lease.Instance.ID
	session.LaunchType = launchType(lease.Instance)
	// A reused browser was ready long before this session arrived. The session's
	// own readiness is the later of the two, otherwise the acquisition latency
	// comes out negative.
	session.ReadyNS = lease.Instance.ReadyNS
	if session.ReadyNS < session.ArrivalNS {
		session.ReadyNS = session.ArrivalNS
	}
	session.CPUShared = lease.Shared
	sharing := r.spool.InUseOn(lease.Instance)

	browser, err := r.ad.OpenSession(ctx, lease.Instance, r.cfg.TargetURL)
	if err != nil {
		r.spool.Give(lease)
		r.settle(session.ID, model.OutcomeResetFailed, "browser context: "+err.Error(), &session)
		return
	}
	session.BrowserContext = browser.ContextID

	session.HoldStartNS = r.now().UnixNano()
	reading, sampled, interrupted := r.hold(ctx, lease.Instance, planned.Hold)
	session.HoldEndNS = r.now().UnixNano()
	// The cgroup counter is the pod's, and this pod carries `sharing` sessions.
	applyReading(&session, divideReading(reading, sharing), sampled)
	if r.rec != nil {
		r.rec.Hold(string(r.cfg.Policy), planned.Bucket, float64(session.HoldEndNS-session.HoldStartNS)/1e9)
		if sampled {
			r.rec.Resources(string(r.cfg.Policy), divideReading(reading, sharing))
		}
	}
	if interrupted {
		// The session was cut short, but the context still has to be released.
		_ = r.ad.CloseSession(ctx, lease.Instance, browser)
		r.spool.Give(lease)
		outcome := model.OutcomeWaiting
		if ctx.Err() == nil {
			outcome = model.OutcomeTimeout
		}
		r.settle(session.ID, outcome, "hold interrupted", &session)
		return
	}

	// Closing the context replaces the old blank-page reset: the next session on
	// this browser starts with no cookies, storage or cache from this one.
	session.ResetStartNS = r.now().UnixNano()
	closeErr := r.ad.CloseSession(ctx, lease.Instance, browser)
	session.ResetEndNS = r.now().UnixNano()
	if r.rec != nil {
		r.rec.Reset(string(r.cfg.Policy), float64(session.ResetEndNS-session.ResetStartNS)/1e9)
	}
	lease.Instance.Sessions++
	r.persistInstance(lease.Instance)

	if r.cfg.Policy == model.PolicyDestroy {
		session.DeleteCalledNS = r.now().UnixNano()
		if err := r.deleteWithRetry(ctx, lease.Instance); err != nil {
			r.settle(session.ID, model.OutcomeDestroyFailed, "release: "+err.Error(), &session)
			r.spool.Retire(lease.Instance)
			return
		}
		session.GoneNS = lease.Instance.DeletedNS
		if r.rec != nil && session.GoneNS > 0 {
			r.rec.Release(string(r.cfg.Policy), float64(session.GoneNS-session.DeleteCalledNS)/1e9)
		}
		r.spool.Retire(lease.Instance)
		r.mu.Lock()
		delete(r.all, lease.Instance.ID)
		r.mu.Unlock()
	} else {
		// Reuse keeps the browser: an idle instance is exactly what the next
		// session needs, and deleting it would make every session a cold start.
		r.spool.Give(lease)
	}

	if closeErr != nil {
		r.settle(session.ID, model.OutcomeResetFailed, "close context: "+closeErr.Error(), &session)
		return
	}
	session.Outcome = model.OutcomeSuccess
	session.Error = ""
	r.settle(session.ID, model.OutcomeSuccess, "", &session)
}

// divideReading turns a pod's counters into a share of the pod, for the case
// where several sessions ran in one Chromium at the same time.
func divideReading(reading Reading, shares int) Reading {
	if shares <= 1 {
		return reading
	}
	div := func(value int64) int64 { return value / int64(shares) }
	return Reading{
		CPUUsec:             div(reading.CPUUsec),
		CPUPressureSomeUsec: div(reading.CPUPressureSomeUsec),
		CPUPressureFullUsec: div(reading.CPUPressureFullUsec),
		ThrottledUsec:       div(reading.ThrottledUsec),
		NRThrottled:         div(reading.NRThrottled),
		MemCurrent:          reading.MemCurrent,
		MemWorkingSet:       reading.MemWorkingSet,
		MemInactiveFile:     reading.MemInactiveFile,
		Shmem:               reading.Shmem,
		Anon:                reading.Anon,
		RefaultAnon:         div(reading.RefaultAnon),
		RefaultFile:         div(reading.RefaultFile),
		SwapCurrent:         reading.SwapCurrent,
		OOMKill:             reading.OOMKill,
		MemPressureSomeUsec: div(reading.MemPressureSomeUsec),
	}
}

// poolShape is what the report and the log say about the slot pool.
func (r *Runner) poolShape() (slots.Snapshot, int) {
	if r.spool == nil {
		return slots.Snapshot{}, 0
	}
	return r.spool.Snapshot(), r.spool.Capacity()
}
