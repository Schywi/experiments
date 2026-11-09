package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/queue"
)

// replayRun reads a recorded run and answers two questions about it: where its
// wait actually went, and whether the simulator reproduces what it recorded.
//
// The split is the level-0 deliverable of the handoff, and it needs no new field:
// arrival→ready minus admission minus claim→ready is the boot permit's share.
// The reproduction is the gate that makes every later comparison credible: a
// simulator that cannot reproduce a recorded FIFO tail is modelling the wrong
// fleet, and its verdict on an algorithm means nothing.
func replayRun(c simulationConfig, dbPath, runID string) error {
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("%s: %w", dbPath, err)
	}
	defer st.Close()

	rows, err := st.Sessions()
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("%s: no sessions stored", dbPath)
	}

	splits := queue.SplitsFor(rows)
	trace, skipped := queue.TraceFromRows(rows, model.ClassNormal)
	if len(trace) == 0 {
		return fmt.Errorf("%s: %d sessions, none with an arrival and a hold", dbPath, len(rows))
	}

	fmt.Printf("queue-lab replay %s\n", filepath.Base(dbPath))
	fmt.Printf("sessions: %d stored, %d replayable, %d skipped (no arrival or no hold)\n\n",
		len(rows), len(trace), skipped)
	printSplit(splits)

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

	fmt.Printf("\nsimulated on the recorded arrivals: %d sessions over %s, mean hold %s\n",
		len(trace), dur(queue.Span(trace)), dur(int64(queue.MeanHold(trace))))
	printSimulation("recorded", trace, c.fleet, results)

	// The fidelity line: the recorded total wait beside the simulated one, so the
	// model's error is printed rather than assumed away.
	sim := results[0]
	fmt.Printf("fidelity: recorded arrival→ready p50 %s p95 %s; %s simulated p50 %s p95 %s\n",
		dur(splits.Total.P50NS), dur(splits.Total.P95NS), sim.Policy,
		dur(queue.Percentile(sim.Waits[model.ClassNormal], 50)),
		dur(queue.Percentile(sim.Waits[model.ClassNormal], 95)))

	dir, err := outDir(c.out)
	if err != nil {
		return err
	}
	payload := struct {
		RunID     string         `json:"run_id"`
		Source    string         `json:"source"`
		Splits    queue.Splits   `json:"splits"`
		Results   []queue.Result `json:"results"`
		Stored    int            `json:"stored"`
		Replay    int            `json:"replayable"`
		Skipped   int            `json:"skipped"`
		ReplayRun string         `json:"replay_of"`
	}{
		RunID: runID, Source: filepath.Base(dbPath), Splits: splits, Results: results,
		Stored: len(rows), Replay: len(trace), Skipped: skipped, ReplayRun: filepath.Base(dbPath),
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, runID+"-replay.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return err
	}
	fmt.Printf("\nwrote %s\n", path)
	return nil
}

// printSplit writes the three gates. It is the table that decides whether the
// experiment is about ordering at all: when boot dominates, a policy moves very
// little and the lever is --boot-concurrency and the warm pool.
func printSplit(s queue.Splits) {
	fmt.Printf("%-24s %6s %9s %9s %9s %9s\n", "wait", "n", "p50", "p95", "max", "mean")
	for _, row := range []struct {
		label string
		s     queue.Split
	}{
		{"admission (gate)", s.Admit},
		{"boot permit (unrecorded)", s.Boot},
		{"slot (claim→ready)", s.Slot},
		{"total (arrival→ready)", s.Total},
	} {
		if row.s.Count == 0 {
			continue
		}
		fmt.Printf("%-24s %6d %9s %9s %9s %9s\n", row.label, row.s.Count,
			dur(row.s.P50NS), dur(row.s.P95NS), dur(row.s.MaxNS), dur(row.s.MeanNS))
	}
	if s.Total.MeanNS > 0 {
		share := func(v int64) float64 { return 100 * float64(v) / float64(s.Total.MeanNS) }
		fmt.Printf("\nshare of the mean wait: admission %.0f%%, boot permit %.0f%%, slot %.0f%%\n",
			share(s.Admit.MeanNS), share(s.Boot.MeanNS), share(s.Slot.MeanNS))
	}
}
