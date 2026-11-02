// Package autoscale is the pool autoscaler: the smallest thing that turns
// pending demand into replicas.
//
// The cluster runs no HPA and no KEDA, and the upstream HPA/KEDA examples scale
// on GKE Managed Prometheus, which this cluster does not have. So the demand
// signal comes from the platform's own objects: claims that are not Ready yet are
// sessions waiting for an instance. That count is the queue, and the pool's
// replica count is the control.
//
// Limits are explicit and configured: --min and --max bound what the autoscaler
// may ask for. The observed capacity is whatever a run measures; this package
// never assumes it.
package pool

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
)

// NodeLoad is the autoscaler's second input: how busy the node is. It is what
// turns demand-only sizing into pressure-aware sizing.
type NodeLoad struct {
	// Utilization is cores in use / cores available, 0..1.
	Utilization float64
	// PressureFull is the CPU PSI "full avg10" value, 0..100: the share of the
	// last ten seconds in which everything was stalled waiting for a core.
	PressureFull float64
}

// NodeLoadFunc reads the node's current load. A nil func, or a func that
// returns an error, keeps demand-only sizing, so the autoscaler still works on
// a host without /proc.
type NodeLoadFunc func() (NodeLoad, error)

// Pressure thresholds. The band between the two pairs is hysteresis: inside it
// the autoscaler holds, so it cannot oscillate every reconcile.
const (
	pressureHigh = 25.0  // above this, relieve the node
	pressureLow  = 5.0   // below this and util below utilLow, grow toward demand
	utilHigh     = 0.85  // cores in use fraction above which to shrink
	utilLow      = 0.70  // below which to grow, when pressure also says free
)

// Config is one autoscaler's settings.
type Config struct {
	Namespace string
	Pool      string
	Min       int32
	Max       int32
	Interval  time.Duration
	// ContextsPerBrowser is how many sessions one replica carries. Demand is
	// converted from sessions to replicas, so a pool is not inflated eightfold
	// when one Chromium serves eight contexts.
	ContextsPerBrowser int
	// ConfigMap is where the settings are published so a run can record what
	// the autoscaler was configured to do.
	ConfigMap string
	Log       *log.Logger
	// NodeLoad reads node CPU utilisation and pressure each step.
	NodeLoad NodeLoadFunc
}

// Settings is the published form: what a run reads back.
type Settings struct {
	Min       int32   `json:"min"`
	Max       int32   `json:"max"`
	IntervalS float64 `json:"interval_s"`
	Namespace string  `json:"namespace"`
	Pool      string  `json:"pool"`
	UpdatedNS int64   `json:"updated_ns"`
	Target    int32   `json:"target"`
	Ready     int32   `json:"ready"`
	Pending   int32   `json:"pending"`
	Failed    int32   `json:"failed,omitempty"`
	Paused    bool    `json:"paused,omitempty"`
	Desired   int32   `json:"desired"`
	// Utilization is the node CPU fraction seen at this step (0..1); empty when
	// no node-load reader is configured.
	Utilization float64 `json:"utilization,omitempty"`
	// PressureFull is the CPU PSI full avg10 percentage seen at this step.
	PressureFull float64 `json:"pressure_full,omitempty"`
}

// Publisher writes the settings somewhere the cluster can read them.
type Publisher interface {
	Publish(ctx context.Context, name string, settings Settings) error
}

// Recorder exports the autoscaler's own series.
type Recorder interface {
	Observe(settings Settings, changed bool, direction string)
	ScaleOut(seconds float64)
}

// Scaler runs one reconcile loop.
type Scaler struct {
	cfg     Config
	ad      sandbox.Adapter
	pub     Publisher
	rec     Recorder
	log     *log.Logger
	scaleUp *time.Time
}

// New builds a scaler.
func New(cfg Config, ad sandbox.Adapter, pub Publisher, rec Recorder) *Scaler {
	logger := cfg.Log
	if logger == nil {
		logger = log.Default()
	}
	return &Scaler{cfg: cfg, ad: ad, pub: pub, rec: rec, log: logger}
}

// Step reconciles once and returns what it saw and did.
func (s *Scaler) Step(ctx context.Context) (Settings, error) {
	stats, err := s.ad.ClaimStats(ctx, s.cfg.Namespace, s.cfg.Pool)
	if err != nil {
		return Settings{}, err
	}
	pool, err := s.ad.Pool(ctx, s.cfg.Namespace, s.cfg.Pool)
	if err != nil {
		return Settings{}, err
	}

	// Demand is every claim that holds an instance plus every claim still
	// waiting for one: that is the pool size that would serve this instant with
	// no queue at all.
	slots := stats.Ready + stats.Pending
	perBrowser := s.cfg.ContextsPerBrowser
	if perBrowser < 1 {
		perBrowser = 1
	}
	demand := int32((int(slots) + perBrowser - 1) / perBrowser)

	// The node's own load is the second input. When it is unavailable the
	// autoscaler sizes on demand alone, which is the historical behaviour and
	// stays correct off-host.
	var load NodeLoad
	if s.cfg.NodeLoad != nil {
		load, err = s.cfg.NodeLoad()
		if err != nil {
			s.log.Printf("autoscaler: node load unavailable: %v; sizing on demand only", err)
			load = NodeLoad{}
		}
	}

	// Pressure-based sizing with hysteresis. Grow toward demand only when the
	// node is clearly free, shrink one step when it is clearly saturated, and
	// hold inside the band so the loop cannot oscillate at the reconcile rate.
	target := demand
	if s.cfg.NodeLoad != nil {
		switch {
		case load.PressureFull > pressureHigh || load.Utilization > utilHigh:
			target = pool.Desired - 1 // relieve the node
		case load.PressureFull < pressureLow && load.Utilization < utilLow:
			target = demand // grow toward what is waiting
		default:
			target = pool.Desired // hold
		}
	}

	// A pool that grows faster than the runner can boot just piles up pending pods,
	// and pending pods hold requests that starve the scheduler. When the runner
	// publishes a boot limit, growth per reconcile is bounded by it.
	if limit := pool.BootConcurrency; limit > 0 && target > pool.Desired+int32(limit) {
		target = pool.Desired + int32(limit)
	}
	if target < s.cfg.Min {
		target = s.cfg.Min
	}
	if target > s.cfg.Max {
		target = s.cfg.Max
	}

	if pool.Paused {
		// The pool belongs to an experiment: publish what was seen, change
		// nothing. This is the supported way to run a fixed-pool experiment
		// while the autoscaler stays deployed.
		settings := Settings{
			Min: s.cfg.Min, Max: s.cfg.Max, IntervalS: s.cfg.Interval.Seconds(),
			Namespace: s.cfg.Namespace, Pool: s.cfg.Pool, UpdatedNS: time.Now().UnixNano(),
			Target: pool.Desired, Ready: stats.Ready, Pending: stats.Pending,
			Failed: stats.Failed, Desired: pool.Desired, Paused: true,
			Utilization: load.Utilization, PressureFull: load.PressureFull,
		}
		if s.pub != nil && s.cfg.ConfigMap != "" {
			if err := s.pub.Publish(ctx, s.cfg.ConfigMap, settings); err != nil {
				return settings, fmt.Errorf("publish settings: %w", err)
			}
		}
		if s.rec != nil {
			s.rec.Observe(settings, false, "")
		}
		return settings, nil
	}

	settings := Settings{
		Min: s.cfg.Min, Max: s.cfg.Max, IntervalS: s.cfg.Interval.Seconds(),
		Namespace: s.cfg.Namespace, Pool: s.cfg.Pool,
		UpdatedNS: time.Now().UnixNano(),
		Target:    target, Ready: stats.Ready, Pending: stats.Pending, Failed: stats.Failed,
		Desired: pool.Desired, Utilization: load.Utilization, PressureFull: load.PressureFull,
	}

	changed := false
	direction := ""
	switch {
	case target > pool.Desired:
		direction = "up"
	case target < pool.Desired:
		direction = "down"
	}
	if direction != "" {
		if err := s.ad.SetPoolReplicas(ctx, s.cfg.Namespace, s.cfg.Pool, target); err != nil {
			return settings, err
		}
		settings.Desired = target
		changed = true
		s.log.Printf("scale %s: desired %d -> %d (pending %d, ready %d, max %d)",
			direction, pool.Desired, target, stats.Pending, stats.Ready, s.cfg.Max)
		if direction == "up" && s.scaleUp == nil {
			now := time.Now()
			s.scaleUp = &now
		}
	}

	// A scale-out ends when the pool is as large as it was asked to be, or when
	// demand drops below the request. The duration is the delay a waiting
	// session actually experiences.
	if s.scaleUp != nil {
		if pool.Ready >= pool.Desired && stats.Pending == 0 {
			elapsed := time.Since(*s.scaleUp).Seconds()
			if s.rec != nil {
				s.rec.ScaleOut(elapsed)
			}
			s.log.Printf("scale-out complete: %d ready after %.1fs", pool.Ready, elapsed)
			s.scaleUp = nil
		}
	}

	if s.pub != nil && s.cfg.ConfigMap != "" {
		if err := s.pub.Publish(ctx, s.cfg.ConfigMap, settings); err != nil {
			return settings, fmt.Errorf("publish settings: %w", err)
		}
	}
	if s.rec != nil {
		s.rec.Observe(settings, changed, direction)
	}
	return settings, nil
}

// Run reconciles until the context ends.
func (s *Scaler) Run(ctx context.Context) error {
	interval := s.cfg.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	s.log.Printf("autoscaler: pool %s/%s min=%d max=%d every %s",
		s.cfg.Namespace, s.cfg.Pool, s.cfg.Min, s.cfg.Max, interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := s.Step(ctx); err != nil {
			s.log.Printf("autoscaler: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
