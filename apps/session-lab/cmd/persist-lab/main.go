// Command persist-lab runs a session workload where a configurable percentage of
// sessions persist their browser state at the end of the session.
//
// It is a separate command on purpose: the existing runner, its flags and
// scripts/session-lab-experiment.sh keep the behaviour they were designed and
// tested for. Everything here goes through the same session.Run, so a persisted
// session takes the same path through the scheduler, the slot pool and the
// browser as any other one; the only addition is the capture before the context
// is disposed.
//
//	persist-lab --sessions 50 --rate 1 --hold 20s --contexts-per-browser 8 \
//	            --persist-rate 0.2 --out /tmp/runs --kubeconfig ~/.kube/config
//
// The capture needs a direct DevTools connection, so this command always asks for
// the ws transport. On the in-pod bridge a context's storage is not reachable.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/kube"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/measure"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/report"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/session"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "persist-lab: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		sessions      = flag.Int("sessions", 0, "number of sessions (required)")
		rate          = flag.Float64("rate", 1, "arrival rate in sessions/s")
		hold          = flag.Duration("hold", 20*time.Second, "session length")
		contexts      = flag.Int("contexts-per-browser", 8, "sessions one Chromium serves")
		persistRate   = flag.Float64("persist-rate", 0.2, "fraction of sessions that persist, 0 to 1")
		persistBudget = flag.Int64("persist-budget", 0, "bytes the bucket may hold; 0 is unbounded")
		restoreRate   = flag.Float64("restore-rate", 0, "fraction of stored blobs to boot after the run, 0 to 1")
		seedMix       = flag.String("seed-mix", "", "datasets per session, e.g. local-64k:10,local-1m:2,idb-256k:3,cache-1m:1,cookies-4:5,none:5")
		seedBytes     = flag.Int("seed-bytes", 0, "sugar for --seed-mix local-<bytes>:1")
		seedItems     = flag.Int("seed-items", 4, "deprecated: a dataset carries its own key count")
		seedCookies   = flag.Int("seed-cookies", 0, "sugar for --seed-mix cookies-<n>:1")
		out           = flag.String("out", "", "output directory for the run database and report (required)")
		kubeconfig    = flag.String("kubeconfig", "", "kubeconfig path; empty uses the default")
		namespace     = flag.String("namespace", "sandbox-demo", "namespace holding the pool and the claims")
		pool          = flag.String("pool", "browser-pool", "SandboxWarmPool name")
		targetURL     = flag.String("target-url", "https://example.com/", "page each session opens")
		runID         = flag.String("run-id", "", "run identifier; defaults to a timestamp")
		cdpPort       = flag.Int("cdp-ws-port", 9224, "port the forwarding sidecar publishes")
		sampleSeconds = flag.Float64("sample-seconds", 10, "interval between resource samples")
	)
	flag.Parse()

	if *sessions <= 0 {
		return fmt.Errorf("--sessions must be positive")
	}
	if *out == "" {
		return fmt.Errorf("--out is required")
	}
	if *persistRate < 0 || *persistRate > 1 {
		return fmt.Errorf("--persist-rate %v is not between 0 and 1", *persistRate)
	}
	abs, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return fmt.Errorf("--out %s: %w", abs, err)
	}
	if *runID == "" {
		*runID = time.Now().UTC().Format("20060102T150405Z")
	}

	cfg := model.RunConfig{
		RunID:              *runID,
		Sessions:           *sessions,
		Rate:               *rate,
		Mix:                []model.Bucket{{Label: hold.String(), Hold: *hold, Count: *sessions}},
		Policy:             model.PolicyReuse,
		Class:              model.ClassNormal,
		Namespace:          *namespace,
		Pool:               *pool,
		ContextsPerBrowser: *contexts,
		CDPTransport:       "ws",
		CDPPort:            *cdpPort,
		TargetURL:          *targetURL,
		TargetURLs:         []string{*targetURL},
		SampleSeconds:      *sampleSeconds,
		OutDir:             abs,
	}

	logger := log.New(os.Stdout, "", log.Ltime|log.Lmicroseconds)

	plan, err := schedule.Generate(cfg, time.Now().UnixNano())
	if err != nil {
		return err
	}

	st, err := store.Open(filepath.Join(abs, cfg.RunID+".db"))
	if err != nil {
		return err
	}
	defer st.Close()

	adapter, err := kube.NewKube(*kubeconfig, "")
	if err != nil {
		return err
	}

	// The corpus: which class of state each session carries. Named apart from the
	// run's schedule.Plan above.
	corpus, err := seedPlan(*seedMix, *seedBytes, *seedItems, *seedCookies, logger)
	if err != nil {
		return err
	}
	if !corpus.Empty() {
		logger.Printf("persist: seed mix %s", corpus)
	}

	// The blob store is the MinIO this cluster runs. Its address and credentials
	// come from the cluster the runner already talks to, so there is no flag to
	// point it elsewhere and no second destination to fall back to.
	blobs, err := openBlobStore(context.Background(), adapter, *namespace, cfg.RunID,
		*persistRate, *persistBudget, corpus, logger)
	if err != nil {
		return err
	}
	logger.Printf("%s", blobs)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := session.New(cfg, session.Options{
		Adapter: adapter,
		Store:   st,
		Plan:    plan,
		Log:     logger,
		Persist: blobs,
		Seeder:  seeder(corpus, logger),
		HostMemory: func() (int64, int64, int64, int64) {
			memory := measure.ReadHostMemory()
			return memory.TotalBytes, memory.AvailableBytes, memory.SwapTotalBytes, memory.SwapFreeBytes
		},
	})
	if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}

	rep, err := report.Build(cfg, plan, st)
	if err != nil {
		return err
	}
	reportPath := filepath.Join(abs, cfg.RunID+".json")
	if err := rep.Write(reportPath); err != nil {
		return err
	}
	rep.Print(os.Stdout)

	// The persistence arm's own numbers, written beside the report and printed as
	// one line a reviewer can read without opening anything.
	stats := blobs.Stats()
	payload, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		return err
	}
	statsPath := filepath.Join(abs, cfg.RunID+"-persist.json")
	if err := os.WriteFile(statsPath, payload, 0o644); err != nil {
		return err
	}
	fmt.Printf("\nstorage: persist-rate %.0f%%, %d blobs, %d bytes retained, %d bytes ingested, %d evicted, mean %.0f B, refused %d, failed %d, budget %d\n",
		stats.Rate*100, stats.Blobs, stats.Bytes, stats.IngestBytes, stats.EvictedBytes,
		stats.MeanBytes, stats.Refused, stats.Failed, stats.Budget)
	logger.Printf("report: %s", reportPath)
	logger.Printf("persist: %s", statsPath)

	// The restore arm boots a session from the blobs this run stored, on a fresh
	// claim: that is the capability the experiment exists to prove.
	if *restoreRate > 0 {
		logger.Printf("restore: booting %.0f%% of the stored blobs", *restoreRate*100)
		restored, err := restoreStored(ctx, adapter, cfg, blobs, *restoreRate, logger)
		if err != nil {
			logger.Printf("restore: %v", err)
		} else {
			fmt.Printf("\n%s\n", restored)
			restorePath := filepath.Join(abs, cfg.RunID+"-restore.json")
			if payload, err := json.MarshalIndent(restored, "", "  "); err != nil {
				logger.Printf("restore: marshal: %v", err)
			} else if err := os.WriteFile(restorePath, payload, 0o644); err != nil {
				logger.Printf("restore: %s: %v", restorePath, err)
			} else {
				logger.Printf("restore: %s", restorePath)
			}
		}
	}
	return nil
}
