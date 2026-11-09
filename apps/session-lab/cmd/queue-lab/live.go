package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/kube"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/config"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/queue"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/report"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/session"
)

// liveConfig is the cluster arm's settings.
type liveConfig struct {
	cfg        simulationConfig
	runID      string
	compare    bool
	admit      int
	maxProv    int
	bootConc   int
	publishCM  string
	kubeconfig string
	namespace  string
	pool       string
	targetURL  string
}

// armQueue adapts the queue experiment's gate to the runner's one seam.
//
// Two things happen here and nowhere else. The session id becomes the client
// identity a policy accumulates service against, and the class is decided by the
// lab's own classifier from the hold the plan carries, so one run can present
// three classes to the queue while model.Session keeps the single class the run
// was configured with.
type armQueue struct {
	gate    *queue.Live
	classOf queue.ClassOf
}

// Acquire implements session.Queue.
func (a *armQueue) Acquire(ctx context.Context, req session.QueueRequest) error {
	return a.gate.Acquire(ctx, queue.Request{
		ID:       req.SessionID,
		ClientID: req.SessionID,
		Class:    a.classOf(req.Bucket, int64(req.Hold)),
		Bucket:   req.Bucket,
	})
}

// Release implements session.Queue.
func (a *armQueue) Release(req session.QueueRequest) {
	a.gate.Release(queue.Request{ID: req.SessionID})
}

// liveRun runs the cluster arm: control first when --compare is set, then the
// named policy, on the same generated trace.
func liveRun(lc liveConfig) error {
	if lc.compare && lc.cfg.kind == "all" {
		return fmt.Errorf("--compare runs one policy; name it with --queue")
	}
	if strings.HasPrefix(lc.cfg.kind, "oracle-") {
		return fmt.Errorf("%s reads the trace's true hold, which a live run cannot do; it is a bound, not a policy", lc.cfg.kind)
	}
	if !lc.compare {
		return runArm(lc, false)
	}
	if err := runArm(lc, false); err != nil {
		return fmt.Errorf("control arm: %w", err)
	}
	if err := runArm(lc, true); err != nil {
		return fmt.Errorf("queue arm: %w", err)
	}
	return nil
}

// runArm runs one workload with or without a queue installed in the runner.
func runArm(lc liveConfig, installed bool) error {
	runID := lc.runID
	if lc.compare {
		suffix := "-control"
		if installed {
			suffix = "-" + lc.cfg.kind
		}
		runID += suffix
	}

	dir, err := outDir(lc.cfg.out)
	if err != nil {
		return err
	}

	plan, trace, err := livePlan(lc)
	if err != nil {
		return err
	}

	cfg := model.RunConfig{
		RunID:              runID,
		Sessions:           len(plan.Sessions),
		Rate:               plan.Rate,
		Mix:                mixOf(plan),
		Policy:             model.PolicyReuse,
		Class:              model.ClassNormal,
		Namespace:          lc.namespace,
		Pool:               lc.pool,
		ContextsPerBrowser: lc.cfg.fleet.SlotsPerPod,
		// Ten seconds, the same interval session-lab samples at. A zero here used
		// to reach time.NewTicker in the hold and panic the run.
		SampleSeconds:   10,
		MaxProvisioning: lc.maxProv,
		DemandPublish:   lc.publishCM,
		CDPTransport:    "http",
		TargetURL:       lc.targetURL,
		TargetURLs:      []string{lc.targetURL},
		OutDir:          dir,
	}

	logger := log.New(os.Stdout, "", log.Ltime|log.Lmicroseconds)
	st, err := store.Open(filepath.Join(dir, runID+".db"))
	if err != nil {
		return err
	}
	defer st.Close()

	adapter, err := kube.NewKube(lc.kubeconfig, "")
	if err != nil {
		return err
	}

	opts := session.Options{Adapter: adapter, Store: st, Plan: plan, Log: logger}
	if lc.publishCM != "" {
		sink, err := newDemandSink(lc.kubeconfig, lc.publishCM, lc.runID)
		if err != nil {
			return err
		}
		opts.DemandSink = sink
		fmt.Printf("%s: publishing queue depth to configmap %s\n", runID, lc.publishCM)
	}

	var arm *armQueue
	if installed {
		policy, err := queue.New(lc.cfg.kind, lc.cfg.options)
		if err != nil {
			return err
		}
		arm = &armQueue{gate: queue.NewLive(policy, lc.admit), classOf: lc.cfg.classOf}
		opts.Queue = arm
		fmt.Printf("%s: queue %s installed, %d admission permit(s)\n", runID, lc.cfg.kind, lc.admit)
	} else {
		fmt.Printf("%s: control arm, admission.Gate ordering and no queue\n", runID)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := session.New(cfg, opts)
	if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}

	rep, err := report.Build(cfg, plan, st)
	if err != nil {
		return err
	}
	repPath := filepath.Join(dir, runID+".json")
	if err := rep.Write(repPath); err != nil {
		return err
	}
	rep.Print(os.Stdout)

	// The queue arm's own numbers: what the gate decided, and where the recorded
	// wait went. The control arm gets the split too, so the two are comparable.
	rows, err := st.Sessions()
	if err != nil {
		return err
	}
	splits := queue.SplitsFor(rows)
	printSplit(splits)

	payload := struct {
		RunID     string        `json:"run_id"`
		Origin    string        `json:"trace_origin"`
		Installed bool          `json:"queue_installed"`
		Policy    string        `json:"policy"`
		Admit     int           `json:"admit"`
		Decisions int64         `json:"decisions,omitempty"`
		Splits    queue.Splits  `json:"splits"`
		Trace     traceShape    `json:"trace"`
		Fleet     fleetShape    `json:"fleet"`
		Simulated *queue.Result `json:"simulated,omitempty"`
	}{
		RunID: runID, Installed: installed, Policy: lc.cfg.kind, Admit: lc.admit,
		Splits: splits,
		Trace:  traceShape{Requests: len(trace), SpanNS: queue.Span(trace), MeanHoldNS: int64(queue.MeanHold(trace))},
		Fleet: fleetShape{
			SlotsPerPod: lc.cfg.fleet.SlotsPerPod, MaxPods: lc.cfg.fleet.MaxPods,
			BootNS: lc.cfg.fleet.Boot, Provisioning: lc.cfg.fleet.Provisioning,
		},
	}
	if arm != nil {
		payload.Decisions = arm.gate.Decisions()
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	queuePath := filepath.Join(dir, runID+"-queue.json")
	if err := os.WriteFile(queuePath, body, 0o644); err != nil {
		return err
	}
	fmt.Printf("\nwrote %s and %s\n", repPath, queuePath)
	return nil
}

// livePlan generates the workload once and returns both the runner's plan and
// the trace the lab records, so the two arms of a comparison share arrivals.
func livePlan(lc liveConfig) (schedule.Plan, []queue.Request, error) {
	run := model.RunConfig{
		RunID:          lc.runID,
		Sessions:       lc.cfg.sessions,
		Rate:           lc.cfg.rate,
		OverloadFactor: lc.cfg.overload,
		FixedArrival:   lc.cfg.fixedArr,
	}
	if lc.cfg.mix != "" {
		buckets, err := config.ParseMix(lc.cfg.mix)
		if err != nil {
			return schedule.Plan{}, nil, err
		}
		run.Mix = buckets
	} else if lc.cfg.sessions > 0 {
		run.Mix = []model.Bucket{{Label: lc.cfg.hold.String(), Hold: lc.cfg.hold, Count: lc.cfg.sessions}}
	} else {
		return schedule.Plan{}, nil, fmt.Errorf("give --mix or --sessions to define the workload")
	}
	if run.Sessions == 0 {
		for _, bucket := range run.Mix {
			run.Sessions += bucket.Count
		}
	}
	plan, err := schedule.Generate(run, lc.cfg.seed)
	if err != nil {
		return schedule.Plan{}, nil, err
	}
	return plan, queue.TraceFromPlan(plan, lc.cfg.classOf), nil
}

// mixOf rebuilds the buckets a plan was generated from, so the report records the
// workload it actually ran.
func mixOf(plan schedule.Plan) []model.Bucket {
	byLabel := map[string]*model.Bucket{}
	order := make([]string, 0, 4)
	for _, planned := range plan.Sessions {
		bucket, ok := byLabel[planned.Bucket]
		if !ok {
			bucket = &model.Bucket{Label: planned.Bucket, Hold: planned.Hold}
			byLabel[planned.Bucket] = bucket
			order = append(order, planned.Bucket)
		}
		bucket.Count++
	}
	out := make([]model.Bucket, 0, len(order))
	for _, label := range order {
		out = append(out, *byLabel[label])
	}
	return out
}
