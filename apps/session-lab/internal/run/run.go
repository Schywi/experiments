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
	"fmt"
	"log"
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

// Options are the runner's collaborators.
type Options struct {
	Adapter adapter.Adapter
	Store   *store.Store
	Sampler Sampler
	Plan    schedule.Plan
	Log     *log.Logger
	Clock   func() time.Time
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
		}
	} else {
		r.log.Printf("boundaries: %v", bErr)
	}
	return r.st.EndRun(r.cfg.RunID, r.now(), r.reps)
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

	if r.cfg.PoolReplicas == nil {
		return nil
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
			if err := r.ad.Resume(ctx, instance); err != nil {
				r.settle(session.ID, model.OutcomeResetFailed, "resume: "+err.Error(), nil)
				r.put(instance)
				return
			}
			r.persistInstance(instance)
		}
	} else {
		name := fmt.Sprintf("lab-%s-%d", r.cfg.RunID, planned.ID)
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
	reading, ok := r.hold(holdCtx, instance, planned.Hold)
	if !ok {
		// A cancelled or expired hold still reports the resources it consumed.
		applyReading(&session, reading, r.smp != nil)
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
	applyReading(&session, reading, r.smp != nil)

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
		if err := r.ad.Release(ctx, instance); err != nil {
			r.settle(session.ID, model.OutcomeDestroyFailed, "release: "+err.Error(), &session)
			return
		}
		session.GoneNS = instance.DeletedNS
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
func (r *Runner) hold(ctx context.Context, instance *adapter.Instance, d time.Duration) (Reading, bool) {
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
			return merged, false
		case <-timer.C:
			sample()
			return merged, true
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

// release deletes an instance on a context that survives run cancellation.
func (r *Runner) release(ctx context.Context, instance *adapter.Instance) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if err := r.ad.Release(releaseCtx, instance); err != nil {
		r.log.Printf("release %s: %v", instance.ID, err)
		return
	}
	r.persistInstance(instance)
	r.mu.Lock()
	delete(r.all, instance.ID)
	r.mu.Unlock()
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
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				state, err := r.ad.Pool(context.WithoutCancel(ctx), r.cfg.Namespace, r.cfg.Pool)
				if err != nil {
					continue
				}
				r.recordReplicas(state, "")
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
