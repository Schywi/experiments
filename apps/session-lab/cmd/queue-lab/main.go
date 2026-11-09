// Command queue-lab runs the queue experiment: which ordering policy lowers the
// wait tail on a pooled browser fleet.
//
// It is a separate command for the reason persist-lab is: session-lab, its flags
// and its drivers keep the behaviour they were designed and tested for. The only
// thing that reaches the runner is one nil-gated field, session.Options.Queue,
// and the control arm in a comparison is that field left nil.
//
// Four modes, none of which needs the others:
//
//	queue-lab --simulate --mix 30s:1500,2m:900,5m:600 --rate 35
//	queue-lab --replay docs/session-lab-runs/<run>.db
//	queue-lab --live --queue=prio-aging --admit=1 --mix ... --out ...
//	queue-lab --compare --queue=prio-aging --mix ... --out ...
//
// --simulate and --replay need no cluster. --live and --compare do, and they run
// in namespace queue-lab against pool queue-lab-pool so a queue run never takes a
// replica a persist or session-lab run is waiting on.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/queue"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "queue-lab: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		kind = flag.String("queue", "fifo",
			"ordering policy: "+join(queue.Kinds)+"; --simulate and --replay accept all")
		simulate = flag.Bool("simulate", false, "replay a generated or recorded trace through every policy, no cluster")
		replayOn = flag.String("replay", "", "path to a session-lab run database; prints its wait split and replays it")
		bootOn   = flag.String("boot-report", "", "path to a run database; reports boot latency per measured concurrency")
		liveOn   = flag.Bool("live", false, "run one arm against the cluster, with --queue installed in the runner")
		compare  = flag.Bool("compare", false, "run the control arm and --queue back to back, same seed")
		traceIn  = flag.String("trace", "", "load a JSON trace instead of generating one")

		sessions  = flag.Int("sessions", 0, "sessions to generate when --mix is absent")
		rate      = flag.Float64("rate", 0, "arrival rate in sessions/s; 0 derives it from --overload-factor")
		hold      = flag.Duration("hold", 30*time.Second, "hold for every session when --mix is absent")
		mix       = flag.String("mix", "", "buckets as label:count, e.g. 30s:1500,2m:900,5m:600")
		overload  = flag.Float64("overload-factor", 1, "offered load as a multiple of one instance's throughput")
		fixedArr  = flag.Bool("fixed-arrival", false, "space arrivals evenly instead of Poisson")
		seed      = flag.Int64("seed", 1, "trace seed; two arms with the same seed share their arrivals")
		estNoise  = flag.Float64("estimate-noise", 0, "spread the arriving estimate by ±this fraction, for the classification arm")
		classes   = flag.String("classes", "three", "class partition: two (normal/bulk, the chosen arm) or three (normal/batch/bulk)")
		classNorm = flag.Duration("class-normal", 30*time.Second, "hold at or below which a session is the normal class")
		classBat  = flag.Duration("class-batch", 2*time.Minute, "hold at or below which a session is the batch class")

		slots     = flag.Int("slots", 0, "fixed concurrent leases, no boot; overrides the elastic fleet")
		perPod    = flag.Int("slots-per-pod", 8, "leases one pod carries")
		maxPods   = flag.Int("max-pods", 38, "pods the fleet may reach")
		boot      = flag.Duration("boot", 40*time.Second, "cold start of one pod")
		provision = flag.Int("provisioning", 1, "pod creations in flight at once; 1 is what slots.Pool does today")
		decide    = flag.Duration("decision-latency", 0, "cost charged per policy decision, to price a remote queue")
		aging     = flag.Duration("aging", 30*time.Second, "waiting time that buys one class rank, for prio-aging")
		demote    = flag.Duration("demote-after", 30*time.Second, "service a client may consume before twolps demotes it")
		wNormal   = flag.Int("weight-normal", 4, "drr weight of the normal class")
		wBatch    = flag.Int("weight-batch", 2, "drr weight of the batch class")
		wBulk     = flag.Int("weight-bulk", 1, "drr weight of the bulk class")

		admitLimit = flag.Int("admit", 1, "admission permits the live gate hands out")
		maxProv    = flag.Int("max-provisioning", 0, "instance creations in flight inside the slot pool; 2+ lets the fleet grow k pods per boot")
		bootConc   = flag.Int("boot-concurrency", 0, "runner boot permit; -1 is explicitly unbounded, 0 derives it from a past measured peak")
		publishCM  = flag.String("publish-demand", "", "publish live queue depth to this ConfigMap (namespace/name)")
		out        = flag.String("out", "docs/queue-lab-runs", "output directory")
		runID      = flag.String("run-id", "", "run identifier; defaults to a timestamp")
		kubeconfig = flag.String("kubeconfig", "", "kubeconfig path; empty uses the default")
		namespace  = flag.String("namespace", "queue-lab", "namespace holding this experiment's pool and claims")
		pool       = flag.String("pool", "queue-lab-pool", "SandboxWarmPool name")
		targetURL  = flag.String("target-url", "https://example.com/", "page each live session opens")
	)
	flag.Parse()

	if *runID == "" {
		*runID = time.Now().UTC().Format("20060102T150405Z")
	}
	classOf := queue.ClassByHold(int64(*classNorm), int64(*classBat))
	switch *classes {
	case "two":
		classOf = queue.ClassByHoldTwo(int64(*classNorm))
	case "three":
	default:
		return fmt.Errorf("--classes %q is two or three", *classes)
	}
	cfg := simulationConfig{
		kind:     *kind,
		sessions: *sessions,
		rate:     *rate,
		hold:     *hold,
		mix:      *mix,
		overload: *overload,
		fixedArr: *fixedArr,
		seed:     *seed,
		estNoise: *estNoise,
		classOf:  classOf,
		classes:  *classes,
		traceIn:  *traceIn,
		fleet: queue.Config{
			Slots:           *slots,
			SlotsPerPod:     *perPod,
			MaxPods:         *maxPods,
			Boot:            int64(*boot),
			Provisioning:    *provision,
			DecisionLatency: int64(*decide),
		},
		options: queue.Options{
			AgingNS:       int64(*aging),
			DemoteAfterNS: int64(*demote),
			Weights: map[model.Class]int{
				model.ClassNormal: *wNormal,
				model.ClassBatch:  *wBatch,
				model.ClassBulk:   *wBulk,
			},
		},
		out: *out,
	}

	switch {
	case *bootOn != "":
		return bootReport(*bootOn, *out, *runID)
	case *replayOn != "":
		return replayRun(cfg, *replayOn, *runID)
	case *simulate && (*liveOn || *compare):
		return fmt.Errorf("--simulate and --live/--compare are different modes; pick one")
	case *liveOn || *compare:
		return liveRun(liveConfig{
			cfg:        cfg,
			runID:      *runID,
			compare:    *compare,
			admit:      *admitLimit,
			maxProv:    *maxProv,
			bootConc:   *bootConc,
			publishCM:  *publishCM,
			kubeconfig: *kubeconfig,
			namespace:  *namespace,
			pool:       *pool,
			targetURL:  *targetURL,
		})
	case *simulate:
		return simulateRun(cfg, *runID)
	default:
		// Simulating is the default: it needs no cluster, so the cheapest mode is
		// what an operator gets by accident rather than the expensive one.
		return simulateRun(cfg, *runID)
	}
}

// simulationConfig is everything the no-cluster modes need.
type simulationConfig struct {
	kind      string
	sessions  int
	rate      float64
	hold      time.Duration
	mix       string
	overload  float64
	fixedArr  bool
	seed      int64
	estNoise  float64
	classes   string
	maxProv   int
	publishCM string
	classOf   queue.ClassOf
	traceIn   string
	fleet     queue.Config
	options   queue.Options
	out       string
}

// policies builds the named policy, or every policy when the name is "all".
func (c simulationConfig) policies() ([]queue.Policy, error) {
	kinds := queue.Kinds
	if c.kind != "all" {
		kinds = []string{c.kind}
	}
	out := make([]queue.Policy, 0, len(kinds))
	for _, kind := range kinds {
		p, err := queue.New(kind, c.options)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// outDir creates the output directory and returns it absolute.
func outDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("--out %s: %w", abs, err)
	}
	return abs, nil
}

func join(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}
