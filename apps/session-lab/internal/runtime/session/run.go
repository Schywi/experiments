// Package run executes a plan against the cluster.
//
// The scheduling rule that matters: an instance is leased to exactly one session
// at a time, and the waiting queue never rejects anyone. A session that the
// platform refuses to serve is recorded with the refusal; a session still waiting
// when the run ends is recorded as waiting. Neither becomes a silent drop.
//
// There is no maximum concurrency here. --workers can bound the goroutines, and
// when it is zero every session gets its own.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/measure"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/persist"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/logging"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/admission"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/slots"
)

// Reading is a resource sample, defined with the reader that produces it.
type Reading = measure.Reading

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
	// SessionUsage records what one browser session cost inside its own context.
	SessionUsage(taskSeconds, jsHeapBytes float64)
	// NodeCPU reports the node's CPU use, from the host sampler.
	NodeCPU(usedCores float64, totalCores int)
	// BootStarted and BootFinished bracket one instance startup, so overlapping
	// spikes are visible as a gauge rather than inferred from latency.
	BootStarted()
	// BootCPU records the peak CPU cores one instance used while starting.
	BootCPU(cores float64)
	BootFinished(seconds float64)
	Pool(desired, ready int32)
	Template(cpuLimitCores, memLimitBytes float64)
	Boundary(kind string)
}

// Options are the runner's collaborators.
type Options struct {
	// Log writes structured run and session events. Optional: a nil Logger is
	// replaced with a discarded one.
	Logger  *logging.Logger
	Adapter sandbox.Adapter
	Store   *store.Store
	Sampler Sampler
	Plan    schedule.Plan
	Log     *log.Logger
	Clock   func() time.Time
	// HostMemory reports the node's memory; nil disables the host series.
	HostMemory func() (total, available, swapTotal, swapFree int64)
	// Recorder exports run progress; nil disables the export.
	Recorder Recorder
	// Persist captures a session's browser state before its context is
	// disposed. Nil disables persistence, which is what every run that does
	// not ask for it gets: the runner's behaviour is unchanged.
	Persist persist.Writer
	// Seeder writes known state into the page before the hold, so a run measures
	// blobs of a realistic size. Nil writes nothing.
	Seeder persist.Seeder
	// AutoscalerInfo returns what autoscaling exists for this run, as JSON. The
	// runner records it rather than being told what to assume.
	AutoscalerInfo func(ctx context.Context) (string, error)
	// Queue replaces the ordering admission.Gate performs. Nil is the default:
	// the gate orders exactly as it always has, and nothing else in the run
	// changes. See queue.go for what a queue may and may not assume.
	Queue Queue
	// DemandSink publishes live queue depth each second so a controller can
	// size the pool from demand. Nil publishes nothing, which is every run that
	// ran before this option existed.
	DemandSink DemandSink
}

// Runner executes one plan.
type Runner struct {
	cfg  model.RunConfig
	ad   sandbox.Adapter
	st   *store.Store
	smp  Sampler
	plan schedule.Plan
	log  *log.Logger
	now  func() time.Time

	start time.Time

	mu     sync.Mutex
	free   []*sandbox.Instance
	all    map[string]*sandbox.Instance
	reps   []store.ReplicaSample
	counts map[model.Outcome]int

	// originalReplicas is each pool's desired count before the run changed it.
	originalReplicas map[string]int32

	// hostMemory reports the node's memory, when a reader was supplied.
	hostMemory func() (total, available, swapTotal, swapFree int64)
	rec        Recorder
	autoscaler func(ctx context.Context) (string, error)

	// persist is the optional capture path; nil means nothing is captured.
	persist persist.Writer
	// seed writes known state into the page before the hold; nil writes nothing.
	seed persist.Seeder

	// minAvailable and memTotal are the node memory extremes the run saw.
	minAvailable   int64
	minAvailableAt int64
	memTotal       int64

	// gate bounds concurrent admissions and orders them by class; queue is the
	// record of who is waiting. Both are built from the run configuration.
	gate  *admission.Gate
	queue *admission.Queue
	// order replaces gate's ordering when the run supplies one. Nil is the
	// default and means the gate decides.
	order Queue
	// demand publishes the live queue depth when the run supplies a sink.
	demand DemandSink
	// boot bounds simultaneous instance startups. Separate from gate: gate
	// bounds admissions, boot bounds the CPU spikes those admissions cause.
	boot *admission.Gate

	// spools hands out browsing slots when one Chromium serves several
	// sessions, keyed by warm pool so a run can route classes to different
	// pools. Nil when the run uses the historical one-session-per-instance path.
	//
	// One goroutine per session calls spoolFor, and an instance retiring walks it
	// from another, so the map has its own lock. It is separate from mu so that no
	// caller of this map can deadlock against a caller of mu: a plain Go map with a
	// concurrent read and write does not merely race, it faults.
	spools      map[string]*slots.Pool
	spoolsMu    sync.Mutex
	instanceSeq int

	// tiers maps a warm pool name to the memory tier its template declares. It is
	// read once in preflight, so an instance records the tier of the pool it came
	// from and a two-cell run attributes swap per cell.
	tiers map[string]string

	// browsers holds one direct CDP connection per instance, used when the run
	// asks for the ws transport.
	browsers map[string]*browser.Conn
	// Node CPU seen by the host sampler: peak and mean, for the report.
	hostCPUPeak    float64
	hostCPUSum     float64
	hostCPUSamples int
	hostCPUTotal   int
	// lg is the structured logger; log is kept for the plain lines that predate it.
	lg *logging.Logger
	// inRun is true between Run's start and its cleanup, so a socket closing
	// during teardown is not reported as a browser failure.
	inRun bool

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
	lg := opts.Logger
	if lg == nil {
		lg = logging.Discard()
	}
	gate := admission.NewGate(cfg.Admit)
	runner := &Runner{
		cfg: cfg, ad: opts.Adapter, st: opts.Store, smp: opts.Sampler, plan: opts.Plan,
		gate: gate, queue: admission.NewQueue(), order: opts.Queue, demand: opts.DemandSink,
		boot: admission.NewGate(cfg.BootConcurrency), browsers: map[string]*browser.Conn{},
		log: logger, lg: lg, now: now, all: map[string]*sandbox.Instance{}, counts: map[model.Outcome]int{},
		hostMemory: opts.HostMemory,
		rec:        opts.Recorder,
		autoscaler: opts.AutoscalerInfo,
		persist:    opts.Persist,
		seed:       opts.Seeder,
	}
	if cfg.ContextsPerBrowser > 1 {
		runner.spools = map[string]*slots.Pool{}
	}
	return runner
}

// Run performs the whole experiment and returns when every session is settled.
// running reports whether the experiment is still executing.
func (r *Runner) running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inRun
}

func (r *Runner) Run(ctx context.Context) error {
	r.start = r.now()
	if r.demand != nil {
		go r.publishDemand(ctx)
	}
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
	r.lg.Info("run start", logging.F("run", r.cfg.RunID), logging.F("sessions", len(r.plan.Sessions)),
		logging.F("rate", r.plan.Rate), logging.F("policy", r.cfg.Policy),
		logging.F("class", r.cfg.Class), logging.F("contexts_per_browser", r.cfg.ContextsPerBrowser),
		logging.F("cdp_transport", r.cfg.CDPTransport), logging.F("admit", r.cfg.Admit),
		logging.F("offered", fmt.Sprintf("%.1f", r.plan.PeakOffered())))

	r.mu.Lock()
	r.inRun = true
	r.mu.Unlock()
	stopSampling := r.sampleReplicas(ctx)

	// A panic anywhere below must not leave claims behind: the platform reads a
	// leftover claim as demand, so the pool grows for a run that no longer
	// exists. Teardown runs first and the panic then continues — swallowing it
	// here would hide the failure that the teardown exists to survive.
	torn := false
	defer func() {
		if rec := recover(); rec != nil {
			stopSampling()
			if !torn {
				torn = true
				if err := r.teardown(ctx); err != nil {
					r.log.Printf("teardown after panic: %v", err)
				}
			}
			panic(rec)
		}
	}()

	dispatchErr := r.dispatch(ctx)
	stopSampling()
	if dispatchErr != nil && ctx.Err() == nil {
		r.log.Printf("dispatch: %v", dispatchErr)
	}
	torn = true
	return r.teardown(ctx)
}

// teardown releases every claim the run made and closes its record. It runs on a
// context that survives a cancelled run, so a SIGINT, an expired budget or a
// panic still leaves no orphaned claims behind for the next experiment to trip
// over. It is the only place that closes a run, so the panic path and the normal
// path cannot each do half of it.
func (r *Runner) teardown(ctx context.Context) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	r.mu.Lock()
	r.inRun = false
	r.mu.Unlock()
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
		NodeCPUPeak:     r.hostCPUPeak, NodeCPUSum: r.hostCPUSum,
		NodeCPUSamples: r.hostCPUSamples, NodeCPUTotalCores: r.hostCPUTotal,
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

	// Boot concurrency is derived from measured spikes when the operator did not
	// pin it. The chicken-and-egg — no peak exists before the first boot — is
	// broken by reading prior runs' peaks from the output directory.
	// A negative value means "explicitly unbounded": the gate is left at
	// NewGate(-1), which admits everyone. Zero still means "derive it from a
	// measured peak when one exists", which is why an experiment that wants k to
	// be the only variable must pass -1 rather than 0 -- with 0 a stale peak from
	// an earlier run silently re-serialises the boot permit.
	if r.cfg.BootConcurrency == 0 {
		if peak, err := r.bootPeak(); err != nil {
			r.log.Printf("preflight: boot peak unavailable: %v", err)
		} else if peak > 0 {
			cores := float64(node.CPUMilli) / 1000
			if k := deriveBootConcurrency(cores, peak); k > 0 {
				r.cfg.BootConcurrency = k
				r.boot = admission.NewGate(k)
				r.log.Printf("preflight: derived boot concurrency %d from measured peak %.2f cores on %.1f cores",
					k, peak, cores)
			}
		}
	}

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
			r.reportCeiling(node, limits)
		}
	}

	if r.cfg.PoolReplicas == nil {
		return nil
	}
	// A fixed-pool run owns every pool it may draw from: the replica count is
	// written to each one and every pool is restored on the way out.
	pools := r.poolNames()
	r.originalReplicas = make(map[string]int32, len(pools))
	for _, name := range pools {
		state, err := r.ad.Pool(ctx, r.cfg.Namespace, name)
		if err != nil {
			return err
		}
		r.originalReplicas[name] = state.Desired
		// Tell any autoscaler this pool is owned by the experiment. Without this
		// the runner and the scaler would both write spec.replicas, and whichever
		// ran last would win.
		if err := r.ad.SetPoolPaused(ctx, r.cfg.Namespace, name, true); err != nil {
			r.log.Printf("preflight: cannot claim pool %s: %v", name, err)
		}
		if err := r.ad.SetBootConcurrency(ctx, r.cfg.Namespace, name, r.cfg.BootConcurrency); err != nil {
			r.log.Printf("preflight: cannot publish boot concurrency on %s: %v", name, err)
		}
		r.log.Printf("preflight: setting pool %s replicas %d -> %d", name, state.Desired, *r.cfg.PoolReplicas)
		if err := r.ad.SetPoolReplicas(ctx, r.cfg.Namespace, name, *r.cfg.PoolReplicas); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(30 * time.Minute)
	for {
		allReady := true
		for _, name := range pools {
			state, err := r.ad.Pool(ctx, r.cfg.Namespace, name)
			if err != nil {
				return err
			}
			r.recordReplicas(state, "resize")
			if state.Ready < state.Desired {
				allReady = false
			}
		}
		if allReady {
			r.log.Printf("preflight: all pools reached desired=%d", *r.cfg.PoolReplicas)
			return nil
		}
		if time.Now().After(deadline) {
			r.log.Printf("preflight: pools still not at desired=%d after 30m; starting anyway", *r.cfg.PoolReplicas)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// systemPods is the node's own workload: k3s, Cilium, the sandbox controller and
// its router. The kubelet's pod cap counts those, so a ceiling computed from the
// cap alone over-states what a run may create.
const systemPods = 12

// ceiling is what a node can host, in browsers, from the two numbers that bind
// here: physical memory against the template's memory *limit*, and the pod cap.
//
// CPU is deliberately absent. This template carries no CPU limit, so a browser
// bursts to whatever the node has and the scheduler gates on requests, not on
// cores; quoting a CPU ceiling would invent a constraint the platform does not
// enforce. Memory is incompressible and has a hard limit, so it is the real one.
type ceiling struct {
	BrowsersByMemory int
	BrowsersByPods   int
	Browsers         int
	NeededBrowsers   int
}

// capacityCeiling does the arithmetic. A zero or absent memory limit is not a
// ceiling the node can compute, so it contributes nothing rather than dividing by
// zero.
func capacityCeiling(memBytes, pods, memLimitBytes int64, offered float64, contexts int) ceiling {
	if contexts < 1 {
		contexts = 1
	}
	var c ceiling
	if memBytes > 0 && memLimitBytes > 0 {
		c.BrowsersByMemory = int(memBytes / memLimitBytes)
	}
	if pods > 0 {
		c.BrowsersByPods = int(pods) - systemPods
		if c.BrowsersByPods < 0 {
			c.BrowsersByPods = 0
		}
	}
	switch {
	case c.BrowsersByMemory > 0 && c.BrowsersByPods > 0:
		c.Browsers = min(c.BrowsersByMemory, c.BrowsersByPods)
	case c.BrowsersByMemory > 0:
		c.Browsers = c.BrowsersByMemory
	default:
		c.Browsers = c.BrowsersByPods
	}
	if offered > 0 {
		c.NeededBrowsers = int(math.Ceil(offered / float64(contexts)))
	}
	return c
}

// reportCeiling says what the node can host next to what the run is about to ask
// for. It is a statement, not a refusal: the operator decides what to run, and
// the runner still tries everything it is asked for. But a run that offers many
// times the ceiling does not measure the browser — it measures the ramp — and the
// 1200-session attempt spent ten minutes and 26 leaked claims discovering that
// nobody had compared the two numbers.
func (r *Runner) reportCeiling(node sandbox.NodeCapacity, limits sandbox.TemplateLimits) {
	offered := r.plan.PeakOffered()
	c := capacityCeiling(node.MemBytes, node.Pods, limits.MemLimitBytes, offered, r.cfg.ContextsPerBrowser)
	if c.Browsers == 0 {
		return
	}
	r.log.Printf("preflight: ceiling ~%d browsers (%d by memory: %dMiB / %dMiB each; %d by pod cap: %d pods - %d system)",
		c.Browsers, c.BrowsersByMemory, node.MemBytes/(1<<20), limits.MemLimitBytes/(1<<20),
		c.BrowsersByPods, node.Pods, systemPods)
	r.log.Printf("preflight: offered %.0f contexts at %d per browser needs ~%d browsers, %d slots",
		offered, r.cfg.ContextsPerBrowser, c.NeededBrowsers, c.Browsers*r.cfg.ContextsPerBrowser)
	if c.NeededBrowsers <= c.Browsers {
		return
	}
	r.log.Printf("preflight: WARNING offered load is %.1fx the node's ceiling: sessions after the first %d will wait, "+
		"and the report will show a capacity boundary rather than a load result",
		float64(c.NeededBrowsers)/float64(c.Browsers), c.Browsers*r.cfg.ContextsPerBrowser)
	if err := r.st.AddEvent(model.Event{AtNS: r.now().UnixNano(), Kind: "preflight_ceiling",
		Detail: fmt.Sprintf("needs %d browsers, ceiling %d (memory %d, pods %d) for %d slots",
			c.NeededBrowsers, c.Browsers, c.BrowsersByMemory, c.BrowsersByPods, c.Browsers*r.cfg.ContextsPerBrowser)}); err != nil {
		r.log.Printf("preflight: record ceiling: %v", err)
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
			Pool:      r.poolFor(planned),
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
		Namespace: r.cfg.Namespace, Template: r.cfg.Template, Pool: r.poolFor(planned),
		Outcome: model.OutcomePending, ArrivalNS: r.start.Add(planned.Offset).UnixNano(),
	}
	if r.cfg.ContextsPerBrowser > 1 {
		r.sessionShared(ctx, planned)
		return
	}

	sampleCtx, stopSampler := context.WithCancel(ctx)
	defer stopSampler()

	instance, _ := r.take()
	if instance != nil {
		session.Instance = instance.ID
		if instance.SuspendedNow() {
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
			_, _, readyNS, _ := instance.Placement()
			session.ReadyNS = readyNS
			session.ResumeEndNS = r.now().UnixNano()
			session.Node = instance.Node
			_, podUID, _, _ := instance.Placement()
			session.PodUID = podUID
			r.persistInstance(instance)
			if r.rec != nil {
				r.rec.Resume(string(r.cfg.Policy), float64(session.ResumeEndNS-session.ResumeStartNS)/1e9)
			}
			r.log.Printf("session %d: resumed %s in %dms", session.ID, instance.ID,
				(session.ResumeEndNS-session.ResumeStartNS)/int64(time.Millisecond))
		}
	} else {
		// Admission: wait for this class's turn before touching the cluster, so
		// a bulk run cannot crowd out interactive work and the number of
		// concurrent acquisitions stays bounded when an operator asks for it.
		name := claimName(r.cfg.RunID, planned.ID)
		if r.queue != nil {
			r.queue.Push(name, r.cfg.Class)
		}
		// The order is admission.Gate unless the run supplied a queue of its own.
		order := QueueRequest{
			SessionID: planned.ID, Claim: name, Bucket: planned.Bucket,
			Hold: planned.Hold, Class: r.cfg.Class,
		}
		admitStart := r.now()
		if err := r.admitOrder(ctx, order); err != nil {
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

		pool := r.poolFor(planned)
		acquired, err := r.ad.Acquire(ctx, r.cfg.Namespace, pool, name, r.cfg.Class)
		r.tagTier(acquired, pool)
		r.releaseOrder(order)
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
		_, _, readyNS, _ := instance.Placement()
		session.ReadyNS = readyNS
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
		_, podUID, _, _ := instance.Placement()
		session.PodUID = podUID
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
	held := r.hold(holdCtx, instance, planned.Hold)
	reading, sampled := held.Reading, held.Sampled
	if held.interrupted() {
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
		instance.CountSession()
		r.persistInstance(instance)
		r.put(instance)
	case model.PolicySuspend:
		if err := r.ad.Suspend(ctx, instance); err != nil {
			r.settle(session.ID, model.OutcomeResetFailed, "suspend: "+err.Error(), &session)
			r.release(ctx, instance)
			return
		}
		instance.CountSession()
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
		instance.CountSession()
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
func (r *Runner) hold(ctx context.Context, instance *sandbox.Instance, d time.Duration) holdResult {
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
	// A zero sample interval means "sample at the boundaries only". A ticker built
	// from it panics — time.NewTicker(0) — which is what a caller that never set
	// SampleSeconds produced; a nil channel blocks in the select below, which is the
	// behaviour that interval describes.
	var ticks <-chan time.Time
	if interval := time.Duration(r.cfg.SampleSeconds * float64(time.Second)); interval > 0 {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			sample()
			return holdResult{Reading: merged, Sampled: have, Completed: false, Reason: "cancelled"}
		case <-timer.C:
			sample()
			return holdResult{Reading: merged, Sampled: have, Completed: true}
		case <-ticks:
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
		// A limit does not move, so the first reading is the answer; a later
		// non-zero value covers a pod whose cgroup was written after it started.
		SwapMax:             carryAllowance(first.SwapMax, next.SwapMax),
		OOMKill:             max(first.OOMKill, next.OOMKill),
		MemPressureSomeUsec: next.MemPressureSomeUsec,
	}
}

// carryAllowance keeps a swap allowance across samples: the first non-zero value
// wins, because a limit written by the node-side writer does not change during a
// session. A pod sampled before the write keeps the later value instead of
// reporting zero.
func carryAllowance(first, next int64) int64 {
	if first != 0 {
		return first
	}
	return next
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
	// The allowance is a limit, not usage: it is written once per pod by the swap
	// experiment's writer and does not grow, so the sampled value is the answer.
	session.SwapAllowanceBytes = reading.SwapMax
	session.OOMKill = reading.OOMKill
	session.MemPressureSomeUsec = reading.MemPressureSomeUsec
	session.Samples++
}

func launchType(instance *sandbox.Instance) string {
	if _, _, _, warm := instance.Placement(); warm {
		return "warm"
	}
	return "cold"
}

// take pops a free instance, if the policy keeps one around.
func (r *Runner) take() (*sandbox.Instance, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.free) == 0 {
		return nil, false
	}
	instance := r.free[len(r.free)-1]
	r.free = r.free[:len(r.free)-1]
	return instance, true
}

func (r *Runner) put(instance *sandbox.Instance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.free = append(r.free, instance)
}

// release deletes an instance on a context that survives run cancellation, and
// retries: a control-plane hiccup at the end of a run must not leave claims
// behind, because a leftover claim looks like demand to an autoscaler and holds
// replicas nobody asked for.
func (r *Runner) release(ctx context.Context, instance *sandbox.Instance) {
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
func (r *Runner) deleteWithRetry(ctx context.Context, instance *sandbox.Instance) error {
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
	instances := make([]*sandbox.Instance, 0, len(r.all))
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

func (r *Runner) persistInstance(instance *sandbox.Instance) {
	sessions, resets, suspends, resumes, bootPeak := instance.Counters()
	node, podUID, readyNS, _ := instance.Placement()
	err := r.st.SaveInstance(model.Instance{
		ID: instance.ID, Kind: string(instance.Kind), Namespace: instance.Namespace,
		Node: node, PodUID: podUID, CreatedNS: instance.CreatedNS,
		ReadyNS: readyNS, DeletedNS: instance.DeletedNS,
		Suspends: suspends, Resumes: resumes,
		Resets: resets, Sessions: sessions,
		BootCPUPeakCores:   bootPeak,
		Class:              classKey(r.cfg.Class),
		ContextsPerBrowser: r.cfg.ContextsPerBrowser,
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

func (r *Runner) recordReplicas(state sandbox.PoolState, note string) {
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
	r.recordNodeCPU()
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

// recordNodeCPU samples the whole node. A pod's cgroup is only readable once it is
// Ready, so this is the series that shows startup spikes colliding.
func (r *Runner) recordNodeCPU() {
	if cpu, err := measure.ReadHostCPU(); err == nil {
		if r.hostCPUTotal == 0 {
			r.hostCPUTotal = cpu.TotalCores
		}
		if cpu.UsedCores > r.hostCPUPeak {
			r.hostCPUPeak = cpu.UsedCores
		}
		r.hostCPUSum += cpu.UsedCores
		r.hostCPUSamples++
		if r.rec != nil {
			r.rec.NodeCPU(cpu.UsedCores, cpu.TotalCores)
		}
	}
}

// storeTemplateLimits writes the template's requests and limits into the run row.
func (r *Runner) storeTemplateLimits(limits sandbox.TemplateLimits) {
	payload, err := json.Marshal(limits)
	if err != nil {
		r.log.Printf("template limits: %v", err)
		return
	}
	if err := r.st.SaveTemplateLimits(r.cfg.RunID, string(payload)); err != nil {
		r.log.Printf("template limits: %v", err)
	}
}

// restorePool puts every pool the run touched back to the replica count it had
// before the run, so an experiment leaves the platform as it found it.
func (r *Runner) restorePool(ctx context.Context) error {
	if r.originalReplicas == nil || r.cfg.PoolReplicas == nil {
		return nil
	}
	for name, original := range r.originalReplicas {
		if original == *r.cfg.PoolReplicas {
			continue
		}
		r.log.Printf("restore: pool %s replicas %d -> %d", name, *r.cfg.PoolReplicas, original)
		if err := r.ad.SetPoolReplicas(ctx, r.cfg.Namespace, name, original); err != nil {
			return err
		}
		if err := r.ad.SetPoolPaused(ctx, r.cfg.Namespace, name, false); err != nil {
			r.log.Printf("restore: release pool %s: %v", name, err)
		}
		if state, err := r.ad.Pool(ctx, r.cfg.Namespace, name); err == nil {
			r.recordReplicas(state, "restore")
		}
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
