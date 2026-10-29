// Command session-lab drives a session workload against the sandbox platform and
// writes what happened.
//
// The program imposes no ceiling on the workload: --sessions, --rate, --mix,
// --workers and --pool-replicas are whatever the operator asks for, and the
// numbers it reports come from the cluster during the run.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/adapter"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/config"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/measure"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/report"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/run"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/store"
)

func main() {
	if err := realMain(); err != nil {
		fmt.Fprintf(os.Stderr, "session-lab: %v\n", err)
		os.Exit(1)
	}
}

func realMain() error {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		return err
	}

	logger := log.New(os.Stdout, "", log.Ltime|log.Lmicroseconds)

	if cfg.DryRun {
		// A smoke test: two short sessions, whatever the policy, so the paths
		// that create and delete real objects get exercised before a long run.
		cfg.Mix = []model.Bucket{{Label: "5s", Hold: 5 * time.Second, Count: 2}}
		cfg.Sessions = 2
		logger.Printf("dry run: 2 sessions of 5s, policy %s", cfg.Policy)
	}

	plan, err := schedule.Generate(cfg.RunConfig, time.Now().UnixNano())
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	kube, err := adapter.NewKube(cfg.Kubeconfig, cfg.Context)
	if err != nil {
		return err
	}

	sampler, err := measure.New(measure.Options{
		CgroupRoot:   cfg.CgroupRoot,
		Interval:     time.Duration(cfg.SampleSeconds * float64(time.Second)),
		Log:          logger,
	})
	if err != nil {
		return err
	}
	logger.Printf("%s", sampler.String())

	// The platform's own numbers are a cross-check on the cgroup readings, and
	// their absence is worth recording: it changes what a report can claim.
	metricsAPI, err := measure.NewMetricsAPI(cfg.Kubeconfig, cfg.Context)
	if err != nil {
		logger.Printf("metrics.k8s.io: %v", err)
	} else if err := metricsAPI.Answering(context.Background(), cfg.Namespace); err != nil {
		logger.Printf("metrics.k8s.io unavailable: %v", err)
		_ = st.AddEvent(model.Event{AtNS: time.Now().UnixNano(), Kind: "metrics_api_unavailable", Detail: err.Error()})
	} else {
		logger.Printf("metrics.k8s.io: answering in %s", cfg.Namespace)
		_ = st.AddEvent(model.Event{AtNS: time.Now().UnixNano(), Kind: "metrics_api_available", Detail: cfg.Namespace})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := run.New(cfg.RunConfig, run.Options{
		Adapter: kube,
		Store:   st,
		Sampler: sampler,
		Plan:    plan,
		Log:     logger,
		HostMemory: func() (int64, int64, int64, int64) {
			memory := measure.ReadHostMemory()
			return memory.TotalBytes, memory.AvailableBytes, memory.SwapTotalBytes, memory.SwapFreeBytes
		},
	})
	runErr := runner.Run(ctx)
	if runErr != nil && ctx.Err() == nil {
		return runErr
	}

	rep, err := report.Build(cfg.RunConfig, plan, st)
	if err != nil {
		return err
	}
	if err := rep.Write(cfg.ReportPath()); err != nil {
		return err
	}
	rep.Print(os.Stdout)
	logger.Printf("report: %s", cfg.ReportPath())
	return nil
}
