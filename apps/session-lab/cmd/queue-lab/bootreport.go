package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/queue"
)

// bootReport reads a run's rows and answers the capacity question the sweep
// exists to settle: how long a cold path takes at each measured level of
// concurrency, how many ready browsers per minute that yields, and what each
// boot was worth.
//
// Concurrency is taken from the recorded intervals, never from the flag the run
// was started with. A run configured for k=4 whose pool serialised shows
// concurrency 1 here, which is the case this report has to make visible.
func bootReport(dbPath, outDir, runID string) error {
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("%s: %w", dbPath, err)
	}
	defer st.Close()

	rows, err := st.Sessions()
	if err != nil {
		return err
	}
	instances, err := st.Instances()
	if err != nil {
		return err
	}
	if len(instances) == 0 {
		return fmt.Errorf("%s: no instances recorded", dbPath)
	}

	boots := make([]queue.Boot, 0, len(instances))
	for _, instance := range instances {
		boots = append(boots, queue.Boot{ID: instance.ID, Created: instance.CreatedNS, Ready: instance.ReadyNS})
	}
	spans := make([]queue.SessionSpan, 0, len(rows))
	for _, row := range rows {
		if row.ArrivalNS <= 0 {
			continue
		}
		spans = append(spans, queue.SessionSpan{
			Instance: row.Instance, Arrival: row.ArrivalNS,
			Ready: row.ReadyNS, HoldStart: row.HoldStartNS, HoldEnd: row.HoldEndNS,
		})
	}

	report := queue.AnalyseBoots(boots, spans)

	fmt.Printf("boot report %s\n", filepath.Base(dbPath))
	fmt.Printf("instances %d, sessions %d, peak measured concurrency %d, served-by-nobody %d\n\n",
		report.Boots, len(spans), report.PeakConcurrency, report.Unused)
	fmt.Printf("%-6s %6s %9s %9s %9s %10s %7s %7s %7s\n",
		"k", "boots", "p50", "p95", "max", "boots/min", "served", "idle", "waited")
	for _, b := range report.Buckets {
		fmt.Printf("%-6d %6d %9s %9s %9s %10.1f %7d %7d %7d\n",
			b.Concurrency, b.Boots, dur(b.P50NS), dur(b.P95NS), dur(b.MaxNS),
			b.PerMinute, b.Served, b.IdleUnused, b.IdleWaited)
	}
	fmt.Printf("\nk = boots in flight at the same time, measured from created/ready timestamps.\n")
	fmt.Printf("idle = ready with nobody waiting, never used.  walked = ready while sessions waited, bound nobody.\n")
	fmt.Printf("waited = a ready browser nobody could bind, which is a defect rather than slack.\n")

	if outDir == "" {
		return nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if runID == "" {
		runID = time.Now().UTC().Format("20060102T150405Z")
	}
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(outDir, runID+"-boot.json")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return err
	}
	fmt.Printf("\nwrote %s\n", path)
	return nil
}
