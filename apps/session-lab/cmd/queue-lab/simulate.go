package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/config"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/queue"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
)

// trace builds the arrivals this run compares. A recorded JSON trace wins,
// because it is the only way to replay a live run exactly; otherwise the mix is
// generated with the same generator the runner uses, so a simulated arm and a
// live arm can be given the same arrivals by using the same seed.
func trace(c simulationConfig) ([]queue.Request, string, error) {
	if c.traceIn != "" {
		t, err := queue.LoadTrace(c.traceIn)
		if err != nil {
			return nil, "", err
		}
		return t, filepath.Base(c.traceIn), nil
	}

	run := model.RunConfig{
		RunID:          "queue-lab",
		Sessions:       c.sessions,
		Rate:           c.rate,
		OverloadFactor: c.overload,
		FixedArrival:   c.fixedArr,
		Class:          model.ClassNormal,
	}
	switch {
	case c.mix != "":
		buckets, err := config.ParseMix(c.mix)
		if err != nil {
			return nil, "", err
		}
		run.Mix = buckets
	case c.sessions > 0:
		run.Mix = []model.Bucket{{Label: c.hold.String(), Hold: c.hold, Count: c.sessions}}
	default:
		return nil, "", errors.New("give --mix, --sessions or --trace to define the workload")
	}
	if run.Sessions == 0 {
		for _, bucket := range run.Mix {
			run.Sessions += bucket.Count
		}
	}

	plan, err := schedule.Generate(run, c.seed)
	if err != nil {
		return nil, "", err
	}
	t := queue.TraceFromPlan(plan, c.classOf)
	origin := fmt.Sprintf("generated seed=%d", c.seed)
	if c.estNoise > 0 {
		// The classification arm: every arrival still carries an estimate, and it
		// is wrong by a known amount. Deterministic, so two arms see the same
		// errors.
		rng := rand.New(rand.NewSource(c.seed))
		for i := range t {
			if t[i].Hold <= 0 {
				continue
			}
			factor := 1 + c.estNoise*(2*rng.Float64()-1)
			t[i].EstHold = int64(float64(t[i].Hold) * factor)
		}
		origin += fmt.Sprintf(", estimate noise ±%.0f%%", c.estNoise*100)
	}
	return t, origin, nil
}

// simulateRun runs every requested policy over one trace and reports what each
// paid. It is the mode that decides the algorithm; nothing here touches a cluster.
func simulateRun(c simulationConfig, runID string) error {
	trace, origin, err := trace(c)
	if err != nil {
		return err
	}
	policies, err := c.policies()
	if err != nil {
		return err
	}

	results := make([]queue.Result, 0, len(policies))
	for _, p := range policies {
		fleet := c.fleet
		fleet.Policy = p
		results = append(results, queue.Simulate(trace, fleet))
	}

	dir, err := outDir(c.out)
	if err != nil {
		return err
	}
	payload := struct {
		RunID   string         `json:"run_id"`
		Origin  string         `json:"trace_origin"`
		Fleet   fleetShape     `json:"fleet"`
		Trace   traceShape     `json:"trace"`
		Results []queue.Result `json:"results"`
	}{
		RunID:  runID,
		Origin: origin,
		Fleet: fleetShape{
			Slots:        c.fleet.Slots,
			SlotsPerPod:  c.fleet.SlotsPerPod,
			MaxPods:      c.fleet.MaxPods,
			BootNS:       c.fleet.Boot,
			Provisioning: c.fleet.Provisioning,
			DecisionNS:   c.fleet.DecisionLatency,
		},
		Trace: traceShape{
			Requests:   len(trace),
			SpanNS:     queue.Span(trace),
			MeanHoldNS: int64(queue.MeanHold(trace)),
		},
		Results: results,
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, runID+"-simulate.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return err
	}
	if err := queue.SaveTrace(filepath.Join(dir, runID+"-trace.json"), trace); err != nil {
		return err
	}

	printSimulation(origin, trace, c.fleet, results)
	fmt.Printf("\nwrote %s and %s\n", path, filepath.Join(dir, runID+"-trace.json"))
	return nil
}

type fleetShape struct {
	Slots        int   `json:"slots,omitempty"`
	SlotsPerPod  int   `json:"slots_per_pod"`
	MaxPods      int   `json:"max_pods"`
	BootNS       int64 `json:"boot_ns"`
	Provisioning int   `json:"provisioning"`
	DecisionNS   int64 `json:"decision_latency_ns"`
}

type traceShape struct {
	Requests   int   `json:"requests"`
	SpanNS     int64 `json:"span_ns"`
	MeanHoldNS int64 `json:"mean_hold_ns"`
}

// printSimulation writes the comparison table: one row per policy and class,
// because a class's tail is the thing a policy moves and an average hides it.
func printSimulation(origin string, trace []queue.Request, fleet queue.Config, results []queue.Result) {
	fmt.Printf("queue-lab simulate   trace %s: %d sessions over %s, mean hold %s\n",
		origin, len(trace), dur(queue.Span(trace)), dur(int64(queue.MeanHold(trace))))
	if fleet.Slots > 0 {
		fmt.Printf("fleet: %d fixed leases, no boot\n", fleet.Slots)
	} else {
		fmt.Printf("fleet: %d leases/pod, max %d pods, boot %s, %d creation in flight\n",
			fleet.SlotsPerPod, fleet.MaxPods, dur(fleet.Boot), fleet.Provisioning)
	}
	if fleet.DecisionLatency > 0 {
		fmt.Printf("decision latency charged: %s\n", dur(fleet.DecisionLatency))
	}

	fmt.Printf("\n%-12s %-7s %5s %9s %9s %9s %9s %8s\n",
		"policy", "class", "n", "p50", "p95", "p99", "max", "starved")
	for _, res := range results {
		for _, row := range queue.Summarise(res) {
			fmt.Printf("%-12s %-7s %5d %9s %9s %9s %9s %8d\n",
				res.Policy, row.Class, row.Count,
				dur(row.P50NS), dur(row.P95NS), dur(row.P99NS), dur(row.MaxNS), row.Starved)
		}
		fmt.Printf("  %s: wait %s over %d sessions, makespan %s, util %.2f, class-jain %.2f, %d decisions\n",
			res.Policy, dur(queue.TotalWaitNS(res)), res.Started, dur(res.Makespan),
			res.Utilisation, res.Fairness, res.Decisions)
		fmt.Println()
	}
}

// dur prints a nanosecond count in the unit a reader can compare across a table.
func dur(ns int64) string {
	switch {
	case ns == 0:
		return "0"
	case ns < int64(time.Millisecond):
		return fmt.Sprintf("%dµs", ns/1e3)
	case ns < int64(time.Second):
		return fmt.Sprintf("%dms", ns/int64(time.Millisecond))
	case ns < int64(time.Minute):
		return fmt.Sprintf("%.1fs", float64(ns)/float64(time.Second))
	default:
		return fmt.Sprintf("%.1fm", float64(ns)/float64(time.Minute))
	}
}
