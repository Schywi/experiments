// Package run executes a plan against the cluster.
//
// The scheduling rule that matters: an instance is leased to exactly one session
// at a time, and the waiting queue never rejects anyone. A session that the
// platform refuses to serve is recorded with the refusal; a session still waiting
// when the run ends is recorded as waiting. Neither becomes a silent drop.
//
// There is no maximum concurrency here. --workers can bound the goroutines, and
// when it is zero every session gets its own.
package run

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/adapter"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/store"
)

// Reading is one resource sample for a sandbox pod.
type Reading struct {
	CPUUsec             int64
	CPUPressureSomeUsec int64
	CPUPressureFullUsec int64
	ThrottledUsec       int64
	NRThrottled         int64
	MemCurrent          int64
	MemWorkingSet       int64
	MemInactiveFile     int64
	Shmem               int64
	Anon                int64
	RefaultAnon         int64
	RefaultFile         int64
	SwapCurrent         int64
	OOMKill             int64
	MemPressureSomeUsec int64
}

// Sampler reads resource counters for one sandbox pod. cumulative counters are
// expected to grow monotonically; the runner differences them per session.
type Sampler interface {
	Sample(ctx context.Context, podUID, node string) (Reading, bool, error)
}

// Recorder receives what the run is doing, for export to a metrics endpoint.
// Every method is nil-safe at the call site, so a run without one still works.
type Recorder interface {
	SessionArrived()
	SessionAcquired()
	SessionSettled(policy string, outcome model.Outcome)
	QueueWait(policy string, seconds float64)
	Acquire(policy, launchType string, seconds float64)
	Hold(policy, bucket string, seconds float64)
	Reset(policy string, seconds float64)
	Release(policy string, seconds float64)
	Resume(policy string, seconds float64)
	Resources(policy string, reading Reading)
	Pool(desired, ready int32)
	Template(cpuLimitCores, memLimitBytes float64)
	Boundary(kind string)
}

// Options are the runner's collaborators.
type Options struct {
	Adapter adapter.Adapter
	Store   *store.Store
	Sampler Sampler
	Plan    schedule.Plan
	Log     *log.Logger
	Clock   func() time.Time
	// HostMemory reports the node's memory; nil disables the host series.
	HostMemory func() (total, available, swapTotal, swapFree int64)
	// Recorder exports run progress; nil disables the export.
	Recorder Recorder
	// AutoscalerInfo returns what autoscaling exists for this run, as JSON. The
	// runner records it rather than being told what to assume.
	AutoscalerInfo func(ctx context.Context) (string, error)
}

// Runner executes one plan.
type Runner struct {
	cfg  model.RunConfig
	ad   adapter.Adapter
	st   *store.Store
	smp  Sampler
	plan schedule.Plan
	log  *log.Logger
	now  func() time.Time

	start time.Time

	mu     sync.Mutex
	free   []*adapter.Instance
	all    map[string]*adapter.Instance
	reps   []store.ReplicaSample
	counts map[model.Outcome]int

	// originalReplicas is the pool's desired count before the run changed it.
	originalReplicas *int32

	// hostMemory reports the node's memory, when a reader was supplied.
	hostMemory func() (total, available, swapTotal, swapFree int64)
	rec        Recorder
	autoscaler func(ctx context.Context) (string, error)

	// minAvailable and memTotal are the node memory extremes the run saw.
	minAvailable   int64
	minAvailableAt int64
	memTotal       int64

	// Runtime facts: the arrival window actually achieved, and the deepest
	// queue and cleanup failures seen.
	firstArrivalNS int64
	lastArrivalNS  int64
	waitingN       int64
	inFlightN      int64
	peakWaiting    int64
	peakInFlight   int64
	cleanupFailed  int64
}

// New assembles a runner.
func New(cfg model.RunConfig, opts Options) *Runner {
	now := opts.Clock
	if now == nil {
		now = time.Now
	}
	logger := opts.Log
	if logger == nil {
		logger = log.Default()
	}
	return &Runner{
		cfg: cfg, ad: opts.Adapter, st: opts.Store, smp: opts.Sampler, plan: opts.Plan,
		log: logger, now: now, all: map[string]*adapter.Instance{}, counts: map[model.Outcome]int{},
		hostMemory: opts.HostMemory,
		rec:        opts.Recorder,
		autoscaler: opts.AutoscalerInfo,
	}
}

// Run performs the whole experiment and returns when every session is settled.
func (r *Runner) Run(ctx context.Context) error {
	r.start = r.now()
	if err := r.st.StartRun(r.cfg, r.start); err != nil {
		return err
	}
	if err := r.preflight(ctx); err != nil {
		return err
	}
	if err := r.insertSessions(); err != nil {
		return err
	}
	r.log.Printf("run %s: %d sessions, %.3f/s, policy %s, mean hold %s, offered concurrency %.1f",
		r.cfg.RunID, len(r.plan.Sessions), r.plan.Rate, r.cfg.Policy, r.plan.MeanHold(), r.plan.PeakOffered())

	stopSampling := r.sampleReplicas(ctx)
	dispatchErr := r.dispatch(ctx)
	stopSampling()
	if dispatchErr != nil && ctx.Err() == nil {
		r.log.Printf("dispatch: %v", dispatchErr)
	}

	// Cleanup runs on a context that survives a cancelled run, so a SIGINT leaves
	// no orphaned claims behind for the next experiment to trip over.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	r.releaseAll(cleanupCtx)
	if r.cfg.PoolReplicas != nil {
		if poolErr := r.restorePool(cleanupCtx); poolErr != nil {
			r.log.Printf("restore pool: %v", poolErr)
		}
	}

	// Anything still pending never started: record it as waiting rather than
	// leaving a row that looks like a success.
	if settleErr := r.settlePending(); settleErr != nil {
		r.log.Printf("settle pending: %v", settleErr)
	}
	if boundaries, bErr := r.ad.Boundaries(cleanupCtx, r.cfg.Namespace, r.start); bErr == nil {
		for _, b := range boundaries {
			r.log.Printf("boundary: %s %s: %s", b.Kind, b.Object, b.Message)
			_ = r.st.AddEvent(model.Event{AtNS: b.At.UnixNano(), Kind: "boundary:" + b.Kind, Detail: b.Object + ": " + b.Message})
			if r.rec != nil {
				r.rec.Boundary(b.Kind)
			}
		}
	} else {
		r.log.Printf("boundaries: %v", bErr)
	}
	r.mu.Lock()
	first, last := r.firstArrivalNS, r.lastArrivalNS
	facts := store.RunRuntime{
		PeakWaiting:     int(r.peakWaiting),
		PeakInFlight:    int(r.peakInFlight),
		CleanupFailures: int(r.cleanupFailed),
		DispatchPlanned: int64(len(r.plan.Sessions)),
	}
	for _, outcome := range r.counts {
		facts.DispatchCompleted += int64(outcome)
	}
	r.mu.Unlock()
	if first > 0 && last > first {
		facts.ArrivalWindowNS = last - first
	}
	return r.st.EndRun(r.cfg.RunID, r.now(), r.reps, facts)
}

// preflight reads what the platform currently reports and, when asked, changes
// the pool's size. The previous replica count is written to the run row so a
// later reader knows what the pool looked like before the experiment.
func (r *Runner) preflight(ctx context.Context) error {
	pool, err := r.ad.Pool(ctx, r.cfg.Namespace, r.cfg.Pool)
	if err != nil {
		return err
	}
	node, err := r.ad.Node(ctx, r.cfg.Namespace)
	if err != nil {
		return err
	}
	r.log.Printf("preflight: pool %s/%s desired=%d ready=%d template=%q",
		r.cfg.Namespace, r.cfg.Pool, pool.Desired, pool.Ready, pool.Template)
	r.log.Printf("preflight: node %s allocatable cpu=%dm memory=%dMiB pods=%d",
		node.Name, node.CPUMilli, node.MemBytes/(1<<20), node.Pods)
	r.recordReplicas(pool, "preflight")

	// Whether anything scales this pool is part of the run's configuration.
	if r.autoscaler != nil {
		info, err := r.autoscaler(ctx)
		if err != nil {
			r.log.Printf("preflight: autoscaler info unavailable: %v", err)
		} else if info != "" {
			if err := r.st.SaveAutoscaler(r.cfg.RunID, info); err != nil {
				r.log.Printf("preflight: record autoscaler: %v", err)
			}
			r.log.Printf("preflight: autoscaler %s", info)
		}
	}

	// What the sandbox is held to belongs with the run: a throttling count
	// without its limit is not a finding.
	templateName := r.cfg.Template
	if templateName == "" {
		templateName = pool.Template
	}
	if templateName != "" {
		limits, err := r.ad.Template(ctx, r.cfg.Namespace, templateName)
		if err != nil {
			r.log.Printf("preflight: template limits unavailable: %v", err)
		} else {
			r.storeTemplateLimits(limits)
			r.log.Printf("preflight: template %s container %s requests cpu=%dm memory=%dMiB limits cpu=%dm memory=%dMiB",
				templateName, limits.Container, limits.CPURequestMilli, limits.MemRequestBytes/(1<<20),
				limits.CPULimitMilli, limits.MemLimitBytes/(1<<20))
			if r.rec != nil {
				r.rec.Template(float64(limits.CPULimitMilli)/1000, float64(limits.MemLimitBytes))
			}
		}
	}

	if r.cfg.PoolReplicas == nil {
		return nil
	}
	// Tell any autoscaler this pool is owned by the experiment. Without this the
	// runner and the scaler would both write spec.replicas, and whichever ran
	// last would win.
	if err := r.ad.SetPoolPaused(ctx, r.cfg.Namespace, r.cfg.Pool, true); err != nil {
		r.log.Printf("preflight: cannot claim the pool: %v", err)
	}
	original := pool.Desired
	r.originalReplicas = &original
	r.log.Printf("preflight: setting pool replicas %d -> %d", original, *r.cfg.PoolReplicas)
	if err := r.ad.SetPoolReplicas(ctx, r.cfg.Namespace, r.cfg.Pool, *r.cfg.PoolReplicas); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Minute)
	for {
		state, err := r.ad.Pool(ctx, r.cfg.Namespace, r.cfg.Pool)
		if err != nil {
			return err
		}
		r.recordReplicas(state, "resize")
		if state.Ready >= state.Desired {
			r.log.Printf("preflight: pool reached desired=%d ready=%d", state.Desired, state.Ready)
			return nil
		}
		if time.Now().After(deadline) {
			r.log.Printf("preflight: pool still at desired=%d ready=%d after 30m; starting anyway",
				state.Desired, state.Ready)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (r *Runner) insertSessions() error {
	for _, planned := range r.plan.Sessions {
		session := model.Session{
			ID:        planned.ID,
			Bucket:    planned.Bucket,
			Policy:    r.cfg.Policy,
			Namespace: r.cfg.Namespace,
			Template:  r.cfg.Template,
			Pool:      r.cfg.Pool,
			Outcome:   model.OutcomePending,
			ArrivalNS: r.start.Add(planned.Offset).UnixNano(),
		}
		if err := r.st.AddSession(session); err != nil {
			return err
		}
	}
	return nil
}

// dispatch starts every session at its arrival offset.
func (r *Runner) dispatch(ctx context.Context) error {
	var wg sync.WaitGroup
	var sem chan struct{}
	if r.cfg.Workers > 0 {
		sem = make(chan struct{}, r.cfg.Workers)
	}
	for _, planned := range r.plan.Sessions {
		wait := time.Until(r.start.Add(planned.Offset))
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				wg.Wait()
				return ctx.Err()
			case <-timer.C:
			}
		}
		wg.Add(1)
		go func(p schedule.Planned) {
			defer wg.Done()
			r.mu.Lock()
			nowNS := r.now().UnixNano()
			if r.firstArrivalNS == 0 {
				r.firstArrivalNS = nowNS
			}
			r.lastArrivalNS = nowNS
			r.waitingN++
			r.inFlightN++
			if r.waitingN > r.peakWaiting {
				r.peakWaiting = r.waitingN
			}
			if r.inFlightN > r.peakInFlight {
				r.peakInFlight = r.inFlightN
			}
			r.mu.Unlock()
			if r.rec != nil {
				r.rec.SessionArrived()
			}
			if sem != nil {
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					r.settle(p.ID, model.OutcomeWaiting, "run cancelled before the session started", nil)
					return
				}
			}
			r.session(ctx, p)
		}(planned)
	}
	wg.Wait()
	return ctx.Err()
}

// session runs one simulated agent from arrival to terminal outcome, writing the
// row at every transition.
func (r *Runner) session(ctx context.Context, planned schedule.Planned) {
	session := model.Session{
		ID: planned.ID, Bucket: planned.Bucket, Policy: r.cfg.Policy,
		Namespace: r.cfg.Namespace, Template: r.cfg.Template, Pool: r.cfg.Pool,
		Outcome: model.OutcomePending, ArrivalNS: r.start.Add(planned.Offset).UnixNano(),
	}
	sampleCtx, stopSampler := context.WithCancel(ctx)
	defer stopSampler()

	instance, _ := r.take()
	if instance != nil {
		session.Instance = instance.ID
		if instance.Suspended {
			session.ResumeStartNS = r.now().UnixNano()
			if err := r.ad.Resume(ctx, instance); err != nil {
				r.settle(session.ID, model.OutcomeResetFailed, "resume: "+err.Error(), nil)
				r.put(instance)
				return
			}
			r.persistInstance(instance)
			// A resume recreates the pod, so the instance is not usable until it
			// reports Ready again.
			if err := r.ad.WaitReady(ctx, instance); err != nil {
				r.settle(session.ID, model.OutcomeAcquisitionFailed, "resume wait: "+err.Error(), nil)
				r.put(instance)
				return
			}
			session.ReadyNS = instance.ReadyNS
			session.ResumeEndNS = r.now().UnixNano()
			session.Node = instance.Node
			session.PodUID = instance.PodUID
			r.persistInstance(instance)
			if r.rec != nil {
				r.rec.Resume(string(r.cfg.Policy), float64(session.ResumeEndNS-session.ResumeStartNS)/1e9)
			}
			r.log.Printf("session %d: resumed %s in %dms", session.ID, instance.ID,
				(session.ResumeEndNS-session.ResumeStartNS)/int64(time.Millisecond))
		}
	} else {
		name := claimName(r.cfg.RunID, planned.ID)
		acquired, err := r.ad.Acquire(ctx, r.cfg.Namespace, r.cfg.Pool, name)
		if err != nil {
			outcome := model.OutcomeAcquisitionFailed
			if ctx.Err() != nil {
				outcome = model.OutcomeWaiting
			}
			r.settle(session.ID, outcome, "acquire: "+err.Error(), nil)
			return
		}
		instance = acquired
		session.Instance = instance.ID
		session.ClaimCreatedNS = r.now().UnixNano()
		r.mu.Lock()
		r.all[instance.ID] = instance
		r.mu.Unlock()

		waitCtx := ctx
		if r.cfg.SessionDeadline > 0 {
			var cancel context.CancelFunc
			waitCtx, cancel = context.WithTimeout(ctx, r.cfg.SessionDeadline)
			defer cancel()
		}
		if err := r.ad.WaitReady(waitCtx, instance); err != nil {
			outcome := model.OutcomeAcquisitionFailed
			switch {
			case ctx.Err() != nil:
				outcome = model.OutcomeWaiting
			case waitCtx.Err() != nil:
				outcome = model.OutcomeTimeout
			}
			r.settle(session.ID, outcome, "ready: "+err.Error(), &session)
			return
		}
		session.ReadyNS = instance.ReadyNS
		session.LaunchType = launchType(instance)
		r.leaveQueue()
		if r.rec != nil {
			r.rec.SessionAcquired()
			if session.ClaimCreatedNS > 0 {
				r.rec.QueueWait(string(r.cfg.Policy), float64(session.ReadyNS-session.ClaimCreatedNS)/1e9)
			}
			r.rec.Acquire(string(r.cfg.Policy), session.LaunchType, float64(session.ReadyNS-session.ArrivalNS)/1e9)
		}
		session.Node = instance.Node
		session.PodUID = instance.PodUID
		r.persistInstance(instance)
		r.save(session)
	}

	session.HoldStartNS = r.now().UnixNano()
	r.save(session)

	holdCtx := sampleCtx
	var cancelHold context.CancelFunc
	if r.cfg.SessionDeadline > 0 {
		holdCtx, cancelHold = context.WithTimeout(sampleCtx, r.cfg.SessionDeadline)
		defer cancelHold()
	}
	reading, completed, sampled := r.hold(holdCtx, instance, planned.Hold)
	if !completed {
		// A cancelled or expired hold still reports the resources it consumed.
		applyReading(&session, reading, sampled)
		session.HoldEndNS = r.now().UnixNano()
		outcome := model.OutcomeLostUnknown
		if ctx.Err() != nil {
			outcome = model.OutcomeWaiting
		} else if holdCtx.Err() != nil {
			outcome = model.OutcomeTimeout
		}
		r.settle(session.ID, outcome, "hold interrupted", &session)
		return
	}
	session.HoldEndNS = r.now().UnixNano()
	applyReading(&session, reading, sampled)
	if r.rec != nil {
		r.rec.Hold(string(r.cfg.Policy), planned.Bucket, float64(session.HoldEndNS-session.HoldStartNS)/1e9)
		if sampled {
			r.rec.Resources(string(r.cfg.Policy), reading)
		}
	}

	switch r.cfg.Policy {
	case model.PolicyReuse:
		session.ResetStartNS = r.now().UnixNano()
		if err := r.ad.Reset(ctx, instance); err != nil {
			session.ResetEndNS = r.now().UnixNano()
			r.settle(session.ID, model.OutcomeResetFailed, "reset: "+err.Error(), &session)
			r.release(ctx, instance)
			return
		}
		session.ResetEndNS = r.now().UnixNano()
		if r.rec != nil {
			r.rec.Reset(string(r.cfg.Policy), float64(session.ResetEndNS-session.ResetStartNS)/1e9)
		}
		instance.Sessions++
		r.persistInstance(instance)
		r.put(instance)
	case model.PolicySuspend:
		if err := r.ad.Suspend(ctx, instance); err != nil {
			r.settle(session.ID, model.OutcomeResetFailed, "suspend: "+err.Error(), &session)
			r.release(ctx, instance)
			return
		}
		instance.Sessions++
		r.persistInstance(instance)
		r.put(instance)
	case model.PolicyDestroy:
		session.DeleteCalledNS = r.now().UnixNano()
		if err := r.deleteWithRetry(ctx, instance); err != nil {
			r.settle(session.ID, model.OutcomeDestroyFailed, "release: "+err.Error(), &session)
			return
		}
		session.GoneNS = instance.DeletedNS
		if r.rec != nil && session.GoneNS > 0 {
			r.rec.Release(string(r.cfg.Policy), float64(session.GoneNS-session.DeleteCalledNS)/1e9)
		}
		instance.Sessions++
		r.persistInstance(instance)
		r.mu.Lock()
		delete(r.all, instance.ID)
		r.mu.Unlock()
	}
	session.Outcome = model.OutcomeSuccess
	session.Error = ""
	r.settle(session.ID, model.OutcomeSuccess, "", &session)
}

// hold sleeps for the session's duration while sampling the instance.
func (r *Runner) hold(ctx context.Context, instance *adapter.Instance, d time.Duration) (Reading, bool, bool) {
	var merged Reading
	var have bool
	sample := func() {
		if r.smp == nil || instance.PodUID == "" {
			return
		}
		reading, ok, err := r.smp.Sample(ctx, instance.PodUID, instance.Node)
		if err != nil || !ok {
			return
		}
		if !have {
			merged = reading
			have = true
			return
		}
		merged = merge(merged, reading)
	}
	sample()
	ticker := time.NewTicker(time.Duration(r.cfg.SampleSeconds * float64(time.Second)))
	defer ticker.Stop()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			sample()
			return merged, false, have
		case <-timer.C:
			sample()
			return merged, true, have
		case <-ticker.C:
			sample()
		}
	}
}

// merge keeps maxima for gauges and the latest value for cumulative counters;
// applyReading differences counters against the first reading of the session.
func merge(first, next Reading) Reading {
	max := func(a, b int64) int64 {
		if a > b {
			return a
		}
		return b
	}
	return Reading{
		CPUUsec:             next.CPUUsec,
		CPUPressureSomeUsec: next.CPUPressureSomeUsec,
		CPUPressureFullUsec: next.CPUPressureFullUsec,
		ThrottledUsec:       next.ThrottledUsec,
		NRThrottled:         next.NRThrottled,
		MemCurrent:          max(first.MemCurrent, next.MemCurrent),
		MemWorkingSet:       max(first.MemWorkingSet, next.MemWorkingSet),
		MemInactiveFile:     next.MemInactiveFile,
		Shmem:               max(first.Shmem, next.Shmem),
		Anon:                max(first.Anon, next.Anon),
		RefaultAnon:         next.RefaultAnon,
		RefaultFile:         next.RefaultFile,
		SwapCurrent:         max(first.SwapCurrent, next.SwapCurrent),
		OOMKill:             max(first.OOMKill, next.OOMKill),
		MemPressureSomeUsec: next.MemPressureSomeUsec,
	}
}

// applyReading writes the sampled integrals into the session row. The counters
// are cumulative from process start, so the session reports the sampled value at
// the end of the hold and the peak gauges seen during it.
func applyReading(session *model.Session, reading Reading, sampled bool) {
	if !sampled {
		return
	}
	session.CPUUsec = reading.CPUUsec
	session.CPUPressureSomeUsec = reading.CPUPressureSomeUsec
	session.CPUPressureFullUsec = reading.CPUPressureFullUsec
	session.ThrottledUsec = reading.ThrottledUsec
	session.NRThrottled = reading.NRThrottled
	session.MemPeakBytes = reading.MemCurrent
	session.MemWorkingSetPeak = reading.MemWorkingSet
	session.MemInactiveFile = reading.MemInactiveFile
	session.ShmemPeak = reading.Shmem
	session.AnonPeak = reading.Anon
	session.RefaultAnon = reading.RefaultAnon
	session.RefaultFile = reading.RefaultFile
	session.SwapPeakBytes = reading.SwapCurrent
	session.OOMKill = reading.OOMKill
	session.MemPressureSomeUsec = reading.MemPressureSomeUsec
	session.Samples++
}

func launchType(instance *adapter.Instance) string {
	if instance.Warm {
		return "warm"
	}
	return "cold"
}

// take pops a free instance, if the policy keeps one around.
func (r *Runner) take() (*adapter.Instance, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.free) == 0 {
		return nil, false
	}
	instance := r.free[len(r.free)-1]
	r.free = r.free[:len(r.free)-1]
	return instance, true
}

func (r *Runner) put(instance *adapter.Instance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.free = append(r.free, instance)
}

// release deletes an instance on a context that survives run cancellation, and
// retries: a control-plane hiccup at the end of a run must not leave claims
// behind, because a leftover claim looks like demand to an autoscaler and holds
// replicas nobody asked for.
func (r *Runner) release(ctx context.Context, instance *adapter.Instance) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if err := r.deleteWithRetry(releaseCtx, instance); err != nil {
		r.log.Printf("release %s: %v", instance.ID, err)
		r.mu.Lock()
		r.cleanupFailed++
		r.mu.Unlock()
		return
	}
	r.persistInstance(instance)
	r.mu.Lock()
	delete(r.all, instance.ID)
	r.mu.Unlock()
}

// deleteWithRetry makes up to five attempts, backing off between them.
func (r *Runner) deleteWithRetry(ctx context.Context, instance *adapter.Instance) error {
	var err error
	for attempt := 1; attempt <= 5; attempt++ {
		if err = r.ad.Release(ctx, instance); err == nil {
			return nil
		}
		if attempt == 5 {
			break
		}
		r.log.Printf("release %s: attempt %d failed: %v", instance.ID, attempt, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * 2 * time.Second):
		}
	}
	return err
}

func (r *Runner) releaseAll(ctx context.Context) {
	r.mu.Lock()
	instances := make([]*adapter.Instance, 0, len(r.all))
	for _, instance := range r.all {
		instances = append(instances, instance)
	}
	r.mu.Unlock()
	for _, instance := range instances {
		r.release(ctx, instance)
	}
}

// settle records the terminal outcome of one session.
func (r *Runner) settle(id int64, outcome model.Outcome, detail string, session *model.Session) {
	row := model.Session{ID: id, Outcome: outcome, Error: detail}
	if session != nil {
		row = *session
		row.Outcome = outcome
		row.Error = detail
	}
	if err := r.st.UpdateSession(row); err != nil {
		r.log.Printf("update session %d: %v", id, err)
	}
	if err := r.st.AddEvent(model.Event{SessionID: id, AtNS: r.now().UnixNano(), Kind: string(outcome), Detail: detail}); err != nil {
		r.log.Printf("event session %d: %v", id, err)
	}
	r.mu.Lock()
	r.counts[outcome]++
	r.inFlightN--
	if r.waitingN > 0 {
		r.waitingN--
	}
	r.mu.Unlock()
	if r.rec != nil {
		r.rec.SessionSettled(string(r.cfg.Policy), outcome)
	}
}

// leaveQueue drops a session out of the waiting count once it holds an instance.
func (r *Runner) leaveQueue() {
	r.mu.Lock()
	if r.waitingN > 0 {
		r.waitingN--
	}
	r.mu.Unlock()
}

// settlePending marks sessions that never started.
func (r *Runner) settlePending() error {
	rows, err := r.st.Sessions()
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Outcome == model.OutcomePending {
			r.settle(row.ID, model.OutcomeWaiting, "run ended before the session started", nil)
		}
	}
	return nil
}

func (r *Runner) save(session model.Session) {
	if err := r.st.UpdateSession(session); err != nil {
		r.log.Printf("update session %d: %v", session.ID, err)
	}
}

func (r *Runner) persistInstance(instance *adapter.Instance) {
	err := r.st.SaveInstance(model.Instance{
		ID: instance.ID, Kind: string(instance.Kind), Namespace: instance.Namespace,
		Node: instance.Node, PodUID: instance.PodUID, CreatedNS: instance.CreatedNS,
		ReadyNS: instance.ReadyNS, DeletedNS: instance.DeletedNS,
		Suspends: instance.Suspends, Resumes: instance.Resumes,
		Resets: instance.Resets, Sessions: instance.Sessions,
	})
	if err != nil {
		r.log.Printf("save instance %s: %v", instance.ID, err)
	}
}

// sampleReplicas records the pool's replica trajectory for the report.
func (r *Runner) sampleReplicas(ctx context.Context) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		r.recordHost()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				state, err := r.ad.Pool(context.WithoutCancel(ctx), r.cfg.Namespace, r.cfg.Pool)
				if err == nil {
					r.recordReplicas(state, "")
				}
				r.recordHost()
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func (r *Runner) recordReplicas(state adapter.PoolState, note string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reps = append(r.reps, store.ReplicaSample{
		AtNS: r.now().UnixNano(), Desired: state.Desired, Ready: state.Ready, Note: note,
	})
	if r.rec != nil {
		r.rec.Pool(state.Desired, state.Ready)
	}
}

// recordHost appends one node-memory reading, which is what shows the run
// approaching the node's limit rather than the platform's.
func (r *Runner) recordHost() {
	if r.hostMemory == nil {
		return
	}
	total, available, swapTotal, swapFree := r.hostMemory()
	if total == 0 && available == 0 {
		return
	}
	sample := model.HostSample{
		AtNS: r.now().UnixNano(), MemTotal: total, MemAvailable: available,
		SwapTotal: swapTotal, SwapFree: swapFree,
	}
	if err := r.st.AddHostSample(sample); err != nil {
		r.log.Printf("host sample: %v", err)
	}
	if r.minAvailable == 0 || available < r.minAvailable {
		r.minAvailable = available
		r.minAvailableAt = sample.AtNS
	}
	if r.memTotal == 0 {
		r.memTotal = total
	}
}

// storeTemplateLimits writes the template's requests and limits into the run row.
func (r *Runner) storeTemplateLimits(limits adapter.TemplateLimits) {
	payload, err := json.Marshal(limits)
	if err != nil {
		r.log.Printf("template limits: %v", err)
		return
	}
	if err := r.st.SaveTemplateLimits(r.cfg.RunID, string(payload)); err != nil {
		r.log.Printf("template limits: %v", err)
	}
}

// restorePool puts the warm pool back to the replica count it had before the
// run, so an experiment leaves the platform as it found it.
func (r *Runner) restorePool(ctx context.Context) error {
	if r.originalReplicas == nil || r.cfg.PoolReplicas == nil || *r.originalReplicas == *r.cfg.PoolReplicas {
		return nil
	}
	r.log.Printf("restore: pool replicas %d -> %d", *r.cfg.PoolReplicas, *r.originalReplicas)
	if err := r.ad.SetPoolReplicas(ctx, r.cfg.Namespace, r.cfg.Pool, *r.originalReplicas); err != nil {
		return err
	}
	if err := r.ad.SetPoolPaused(ctx, r.cfg.Namespace, r.cfg.Pool, false); err != nil {
		r.log.Printf("restore: release the pool: %v", err)
	}
	state, err := r.ad.Pool(ctx, r.cfg.Namespace, r.cfg.Pool)
	if err == nil {
		r.recordReplicas(state, "restore")
	}
	return nil
}

// Counts returns the outcome tally.
func (r *Runner) Counts() map[model.Outcome]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[model.Outcome]int, len(r.counts))
	for k, v := range r.counts {
		out[k] = v
	}
	return out
}

// claimName builds a DNS-1123 subdomain for a session's claim. The run id can
// carry uppercase letters (a UTC timestamp), which Kubernetes rejects, so the
// name is lowercased and anything outside [a-z0-9-] becomes a dash.
func claimName(runID string, id int64) string {
	cleaned := make([]rune, 0, len(runID))
	for _, r := range strings.ToLower(runID) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cleaned = append(cleaned, r)
		case r == '-', r == '.':
			cleaned = append(cleaned, '-')
		}
	}
	name := fmt.Sprintf("lab-%s-%d", strings.Trim(string(cleaned), "-"), id)
	if len(name) > 63 {
		name = name[len(name)-63:]
	}
	return name
}
