package session

// The shared-browser path: one Chromium serves several sessions, each in its own
// browser context. Capacity is requested only when every slot the pool knows
// about is taken, so the claim count tracks *deficits*, not sessions.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/logging"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/slots"
)

// acquireInstance is the pool's provider: it is the only place the shared path
// reaches the capacity plane, and it runs only when a session has no slot. The
// pool name is chosen by the session that needed the slot, not by the runner.
func (r *Runner) acquireInstance(ctx context.Context, pool string) (*sandbox.Instance, error) {
	r.mu.Lock()
	r.instanceSeq++
	seq := int64(r.instanceSeq)
	r.mu.Unlock()

	name := claimName(r.cfg.RunID, seq)
	r.log.Printf("capacity: no free slot, acquiring %s from %s", name, pool)

	instance, err := r.acquireWithRetry(ctx, name, pool)
	if err != nil {
		return nil, fmt.Errorf("acquire: %w", err)
	}

	waitCtx := ctx
	if r.cfg.SessionDeadline > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, r.cfg.SessionDeadline)
		defer cancel()
	}
	bootDone := make(chan struct{})
	go r.sampleBoot(waitCtx, instance, bootDone)

	if err := r.ad.WaitReady(waitCtx, instance); err != nil {
		close(bootDone)
		// Never leave a claim behind for a slot that cannot be used.
		_ = r.deleteWithRetry(ctx, instance)
		return nil, fmt.Errorf("ready: %w", err)
	}

	close(bootDone)
	r.mu.Lock()
	r.all[instance.ID] = instance
	r.mu.Unlock()
	r.persistInstance(instance)
	r.log.Printf("capacity: %s ready (%s)", instance.ID, launchType(instance))
	return instance, nil
}

// acquireWithRetry creates a claim, retrying a transport failure a few times. A
// single-node control plane under load answers POSTs with EOF or a deadline; that
// is a property of the cluster, not an outcome of the session, and failing the
// session on it turns control-plane noise into a fake capacity boundary.
func (r *Runner) acquireWithRetry(ctx context.Context, name, pool string) (*sandbox.Instance, error) {
	delay := 200 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= 5; attempt++ {
		instance, err := r.ad.Acquire(ctx, r.cfg.Namespace, pool, name, r.cfg.Class)
		if err == nil {
			r.tagTier(instance, pool)
			if attempt > 1 {
				r.lg.Info("claim created after retry", logging.F("claim", name),
					logging.F("attempt", attempt))
			}
			return instance, nil
		}
		lastErr = err
		if !transient(err) || attempt == 5 || ctx.Err() != nil {
			break
		}
		r.lg.Warn("claim creation failed, retrying", logging.F("claim", name),
			logging.F("attempt", attempt), logging.F("retry_in", delay), logging.F("error", err))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay < 2*time.Second {
			delay *= 2
		}
	}
	return nil, lastErr
}

// transient reports whether an API failure is worth retrying: transport errors and
// server-side timeouts, not a refusal the scheduler will repeat verbatim.
func transient(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"unexpected eof", "eof", "connection refused", "connection reset",
		"timeout", "deadline exceeded", "temporarily unavailable",
		"500 internal server error", "502", "503", "504", "apiserver not ready",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// sessionShared runs one session inside a browser context on a shared Chromium,
// retrying on a different instance when the browser's transport fails.
func (r *Runner) sessionShared(ctx context.Context, planned schedule.Planned) {
	attempts := r.cfg.Retries + 1
	for attempt := 1; attempt <= attempts; attempt++ {
		if !r.sessionSharedAttempt(ctx, planned, attempt) {
			return
		}
		if ctx.Err() != nil {
			// The run is ending; the session is recorded as waiting by the caller.
			return
		}
		r.lg.Warn("session retrying on another instance",
			logging.F("session", planned.ID), logging.F("attempt", attempt+1),
			logging.F("of", attempts))
	}
}

// sessionSharedAttempt is one attempt. It reports whether the attempt failed in a
// way that is worth repeating on a different instance; the failed instance has
// already left the pool when it returns true.
func (r *Runner) sessionSharedAttempt(ctx context.Context, planned schedule.Planned, attempt int) (retry bool) {
	pool := r.poolFor(planned)
	spool := r.spoolFor(pool)
	session := model.Session{
		ID: planned.ID, Bucket: planned.Bucket, Policy: r.cfg.Policy,
		Namespace: r.cfg.Namespace, Template: r.cfg.Template, Pool: pool,
		Outcome: model.OutcomePending, ArrivalNS: r.start.Add(planned.Offset).UnixNano(),
		Attempts: attempt,
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

	// One boot at a time per permit: a cold Chromium spikes, and overlapping
	// spikes saturate the node while average CPU still looks idle.
	if err := r.boot.Acquire(ctx, r.cfg.Class); err != nil {
		r.settle(session.ID, model.OutcomeWaiting, "boot permit: "+err.Error(), nil)
		return
	}
	bootStart := r.now()
	if r.rec != nil {
		r.rec.BootStarted()
	}
	releaseBoot := func() {
		if r.rec != nil {
			r.rec.BootFinished(r.now().Sub(bootStart).Seconds())
		}
		r.boot.Release()
	}

	session.ClaimCreatedNS = r.now().UnixNano()
	lease, err := spool.Take(ctx)
	if err != nil {
		releaseBoot()
		outcome := model.OutcomeAcquisitionFailed
		if ctx.Err() != nil {
			outcome = model.OutcomeWaiting
		}
		// No slot and nothing that could be created: the pool is out of healthy
		// capacity, which is a boundary worth naming rather than leaving to be
		// inferred from a pile of acquisition failures.
		r.noteStarved(ctx, pool, err)
		r.settle(session.ID, outcome, "slot: "+err.Error(), nil)
		return
	}
	session.Instance = lease.Instance.ID
	session.LaunchType = launchType(lease.Instance)
	// ReadyNS is when this session got a usable slot, which is what a caller
	// waits for. A reused browser was ready earlier; stamping the instance's own
	// readiness made the acquisition latency structurally zero.
	session.ReadyNS = r.now().UnixNano()
	session.CPUShared = lease.Shared
	sharing := spool.InUseOn(lease.Instance)
	r.lg.Debug("slot", logging.F("session", session.ID), logging.F("instance", lease.Instance.ID),
		logging.F("slot", lease.Index), logging.F("sharing", sharing),
		logging.F("warm", session.LaunchType))

	isolatedState, err := r.openIsolated(ctx, lease.Instance, session.ID)
	releaseBoot()
	if err != nil {
		r.lg.Error("isolate failed", logging.F("session", session.ID),
			logging.F("instance", lease.Instance.ID), logging.F("transport", r.cfg.CDPTransport),
			logging.F("error", err))
		// The browser did not answer on the port the run uses. Returning its slot
		// keeps it in the pool as the instance with the most free capacity, so the
		// next session is handed the same broken browser: retire it instead.
		spool.RetireAndGive(lease)
		if attempt < r.cfg.Retries+1 && browserUnusable(err) {
			return true
		}
		r.settle(session.ID, model.OutcomeResetFailed, "browser context: "+err.Error(), &session)
		return
	}
	session.BrowserContext = string(isolatedState.context)
	r.lg.Debug("context opened", logging.F("session", session.ID),
		logging.F("instance", lease.Instance.ID), logging.F("context", session.BrowserContext),
		logging.F("target", string(isolatedState.target)), logging.F("transport", r.cfg.CDPTransport))

	// Known state goes into the page before the hold, so the capture has
	// something to weigh. A run without a seeder writes nothing here. The page
	// has to have committed its own document first: localStorage on the initial
	// about:blank throws, and the write would vanish with the navigation.
	if r.seed != nil && !isolatedState.viaHTTP {
		if err := isolatedState.session.WaitForLoad(ctx); err != nil {
			r.lg.Warn("persist seed: page never loaded", logging.F("session", session.ID),
				logging.F("error", err.Error()))
		} else if err := r.seed.Seed(ctx, isolatedState.session, session.ID); err != nil {
			r.lg.Warn("persist seed failed", logging.F("session", session.ID),
				logging.F("error", err.Error()))
		}
	}

	session.HoldStartNS = r.now().UnixNano()
	held := r.hold(ctx, lease.Instance, planned.Hold)
	reading, sampled := held.Reading, held.Sampled
	session.HoldEndNS = r.now().UnixNano()
	// The cgroup counter is the pod's, and this pod carries `sharing` sessions.
	applyReading(&session, divideReading(reading, sharing), sampled)
	if r.rec != nil {
		r.rec.Hold(string(r.cfg.Policy), planned.Bucket, float64(session.HoldEndNS-session.HoldStartNS)/1e9)
		if sampled {
			r.rec.Resources(string(r.cfg.Policy), divideReading(reading, sharing))
		}
	}
	if held.interrupted() {
		// The hold was cut short, but the context still has to be released.
		r.lg.Warn("hold interrupted", logging.F("session", session.ID),
			logging.F("instance", lease.Instance.ID), logging.F("reason", held.Reason),
			logging.F("hold_ms", (session.HoldEndNS-session.HoldStartNS)/1e6))
		_, _ = r.closeIsolated(ctx, lease.Instance, isolatedState)
		lease.Instance.Sessions++
		r.persistInstance(lease.Instance)
		spool.Give(lease)
		outcome := model.OutcomeWaiting
		if ctx.Err() == nil {
			outcome = model.OutcomeTimeout
		}
		r.settle(session.ID, outcome, "hold interrupted", &session)
		return
	}

	// A session that persists writes its state here, before the context is
	// disposed: closeIsolated below is what destroys it. Only the ws transport
	// exposes a context's storage, so a run on the in-pod bridge captures
	// nothing. A session whose hold was interrupted returns above and captures
	// nothing, and neither does one whose context never opened.
	if r.persist != nil && !isolatedState.viaHTTP {
		if err := r.persist.Capture(ctx, isolatedState.conn, isolatedState.session,
			isolatedState.context, session.ID); err != nil {
			r.lg.Warn("persist capture failed", logging.F("session", session.ID),
				logging.F("error", err.Error()))
		}
	}

	// Closing the context replaces the old blank-page reset: the next session on
	// this browser starts with no cookies, storage or cache from this one.
	session.ResetStartNS = r.now().UnixNano()
	cost, closeErr := r.closeIsolated(ctx, lease.Instance, isolatedState)
	applyUsage(&session, cost)
	r.lg.Debug("context closed", logging.F("session", session.ID),
		logging.F("instance", lease.Instance.ID),
		logging.F("task_ms", int64(cost.TaskDurationSeconds*1000)),
		logging.F("script_ms", int64(cost.ScriptDurationSeconds*1000)),
		logging.F("heap_kib", int64(cost.JSHeapUsedBytes)/1024),
		logging.F("hold_ms", (session.HoldEndNS-session.HoldStartNS)/1e6))
	if r.rec != nil && (cost.TaskDurationSeconds > 0 || cost.JSHeapUsedBytes > 0) {
		r.rec.SessionUsage(cost.TaskDurationSeconds, cost.JSHeapUsedBytes)
	}
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
			spool.Retire(lease.Instance)
			return
		}
		session.GoneNS = lease.Instance.DeletedNS
		if r.rec != nil && session.GoneNS > 0 {
			r.rec.Release(string(r.cfg.Policy), float64(session.GoneNS-session.DeleteCalledNS)/1e9)
		}
		spool.Retire(lease.Instance)
		r.forgetBrowser(lease.Instance)
		r.mu.Lock()
		delete(r.all, lease.Instance.ID)
		r.mu.Unlock()
	} else if closeErr != nil && browserUnusable(closeErr) {
		// The browser stopped answering while this session held it. Its other
		// contexts are dead too, so the instance leaves the pool here rather than
		// being offered to the next session.
		spool.RetireAndGive(lease)
	} else {
		// Reuse keeps the browser: an idle instance is exactly what the next
		// session needs, and deleting it would make every session a cold start.
		spool.Give(lease)
	}

	if closeErr != nil {
		// A browser that died takes its sessions with it. Reporting that as a
		// reset failure hides the cause: the pod was killed (OOM is the usual
		// reason) and every context in it went with it.
		outcome := model.OutcomeResetFailed
		if browserLost(closeErr) {
			outcome = model.OutcomeLostUnknown
			r.lg.Error("browser lost", logging.F("session", session.ID),
				logging.F("instance", lease.Instance.ID), logging.F("error", closeErr))
		} else {
			r.lg.Error("context close failed", logging.F("session", session.ID),
				logging.F("instance", lease.Instance.ID), logging.F("error", closeErr))
		}
		// The hold completed on this browser, so a transport failure here is worth
		// one more attempt somewhere else rather than a lost session.
		if attempt < r.cfg.Retries+1 && browserUnusable(closeErr) {
			return true
		}
		r.settle(session.ID, outcome, "close context: "+closeErr.Error(), &session)
		return
	}
	session.Outcome = model.OutcomeSuccess
	session.Error = ""
	r.settle(session.ID, model.OutcomeSuccess, "", &session)
	return false
}

// noteStarved records that the pool ran out of usable capacity: every instance it
// held was full or unhealthy, and creating another one failed. It is the signal
// saturation is meant to leave behind, so a run does not have to be read as a pile
// of acquisition failures to see it.
func (r *Runner) noteStarved(ctx context.Context, pool string, cause error) {
	if ctx.Err() != nil {
		// The run is ending; a cancelled acquisition is not starvation.
		return
	}
	if r.rec != nil {
		r.rec.Boundary("slot_starvation")
	}
	if err := r.st.AddEvent(model.Event{
		AtNS: r.now().UnixNano(), Kind: "boundary:slot_starvation",
		Detail: "pool " + pool + ": " + cause.Error(),
	}); err != nil {
		r.log.Printf("event slot_starvation: %v", err)
	}
	r.log.Printf("slot starvation in %s: %v", pool, cause)
}

// browserUnusable reports whether an error means the browser on the other end is
// gone or no longer answering on its CDP port. It is the wider test than
// browserLost: run B's failure was "connection refused" at the dial, which the
// EOF markers do not cover, and an instance that answers that way must leave the
// pool rather than be handed the next session.
func browserUnusable(err error) bool {
	if err == nil {
		return false
	}
	if browserLost(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection refused", "no route to host", "connection reset",
		"i/o timeout", "timeout", "deadline exceeded", "temporarily unavailable",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// browserLost reports whether an error means the Chromium connection ended: the
// pod was killed, the process crashed, or the sidecar went away. Those are not
// per-session failures and must not be counted as reset problems.
func browserLost(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"eof", "connection reset", "broken pipe", "connection closed", "use of closed network connection"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// applyUsage records what the browser reported about this session's own context.
func applyUsage(session *model.Session, cost browser.Usage) {
	session.TaskDurationUS = int64(cost.TaskDurationSeconds * 1e6)
	session.ScriptDurationUS = int64(cost.ScriptDurationSeconds * 1e6)
	session.JSHeapBytes = int64(cost.JSHeapUsedBytes)
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
		// Memory is the pod's, exactly like CPU: a session row must not claim the
		// whole browser's footprint, or 30 sessions look like 10 GiB.
		MemCurrent:      div(reading.MemCurrent),
		MemWorkingSet:   div(reading.MemWorkingSet),
		MemInactiveFile: div(reading.MemInactiveFile),
		Shmem:           div(reading.Shmem),
		Anon:            div(reading.Anon),
		RefaultAnon:     div(reading.RefaultAnon),
		RefaultFile:     div(reading.RefaultFile),
		SwapCurrent:     div(reading.SwapCurrent),
		// The allowance is a limit on the pod, not usage, so it is not divided:
		// every session in a shared browser was given the same ceiling.
		SwapMax:             reading.SwapMax,
		OOMKill:             reading.OOMKill,
		MemPressureSomeUsec: div(reading.MemPressureSomeUsec),
	}
}

// poolShape is what the report and the log say about the slot pools, summed
// across every warm pool the run may draw from.
func (r *Runner) poolShape() (slots.Snapshot, int) {
	if r.spools == nil {
		return slots.Snapshot{}, 0
	}
	var total slots.Snapshot
	for _, spool := range r.spools {
		snap := spool.Snapshot()
		total.Instances += snap.Instances
		total.Slots += snap.Slots
		total.InUse += snap.InUse
		total.IdleInstances += snap.IdleInstances
	}
	return total, r.cfg.ContextsPerBrowser
}
