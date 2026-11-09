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

// Demand is what the runner publishes about its own queue. It is the only input
// that comes from the process holding the waiters: claims cannot carry it,
// because a session waiting in the gate or the slot pool has no claim.
type Demand struct {
	Live     int `json:"live"`
	Waiting  int `json:"waiting"`
	Contexts int `json:"contexts_per_browser"`
}

// DemandReader reads the published demand. A nil reader keeps sizing on claims.
type DemandReader func(ctx context.Context) (Demand, bool, error)

// NodeLoad is the autoscaler's second input: how busy the node is. It is what
// turns demand-only sizing into pressure-aware sizing.
type NodeLoad struct {
	// Utilization is cores in use / cores available, 0..1.
	Utilization float64
	// PressureFull is the CPU PSI "full avg10" value, 0..100: the share of the
	// last ten seconds in which everything was stalled waiting for a core.
	PressureFull float64
	// MemAvailableMB and MemTotalMB are the node's memory, and
	// MemoryPressureFull is the memory PSI full avg10. Zero means not read: a
	// run on a host without /proc keeps sizing on CPU and demand.
	MemAvailableMB     float64
	MemTotalMB         float64
	MemoryPressureFull float64
}

// NodeLoadFunc reads the node's current load. A nil func, or a func that
// returns an error, keeps demand-only sizing, so the autoscaler still works on
// a host without /proc.
type NodeLoadFunc func() (NodeLoad, error)

// Pressure thresholds. The band between the two pairs is hysteresis: inside it
// the autoscaler holds, so it cannot oscillate every reconcile.
const (
	pressureHigh = 25.0 // above this, relieve the node
	pressureLow  = 5.0  // below this and util below utilLow, grow toward demand
	utilHigh     = 0.85 // cores in use fraction above which to shrink
	utilLow      = 0.70 // below which to grow, when pressure also says free
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
	// NodeLoad reads node CPU utilisation, pressure and memory each step.
	NodeLoad NodeLoadFunc
	// DemandSource is "claims" (the default, and the behaviour before this
	// field) or "published". Published demand is read through DemandReader and
	// falls back to claims when the runner has not published anything yet.
	DemandSource string
	DemandReader DemandReader
	// MemoryReserveMB holds growth when MemAvailable falls below it, and
	// MemPressureHigh holds growth when memory PSI full exceeds it. Zero
	// disables each term, which is the behaviour of every autoscaler before
	// this field existed.
	MemoryReserveMB float64
	MemPressureHigh float64
	// MaxProvisioning and ImageTag are published, not used: they are what a run
	// records about the arm it ran, so two arms are distinguishable from their
	// own ConfigMap.
	MaxProvisioning int
	ImageTag        string
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
	// MemAvailableMB and MemoryPressureFull are the memory inputs, when read.
	MemAvailableMB     float64 `json:"mem_available_mb,omitempty"`
	MemoryPressureFull float64 `json:"memory_pressure_full,omitempty"`
	// ImageTag and MaxProvisioning identify the arm: a run records the ConfigMap
	// it found, so a v1 and a v2 run cannot be confused afterwards.
	ImageTag        string `json:"image_tag,omitempty"`
	MaxProvisioning int    `json:"max_provisioning,omitempty"`
	// DemandSource is which input sized the pool, and DemandPods is what it said.
	DemandSource string `json:"demand_source,omitempty"`
	DemandPods   int32  `json:"demand_pods,omitempty"`
	// DemandFallback is set when published demand was asked for and claims were
	// used instead, so a report cannot read a fallback as a published number.
	DemandFallback bool `json:"demand_fallback,omitempty"`
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

	perBrowser := s.cfg.ContextsPerBrowser
	if perBrowser < 1 {
		perBrowser = 1
	}

	// Claims describe the fleet, not the queue: a session waiting in the
	// admission gate or the slot pool has no claim, and a claim bound to a
	// booting warm-pool sandbox is bound rather than pending. "published" reads
	// the runner's own counts instead and falls back to claims until the first
	// publish arrives.
	slots := stats.Ready + stats.Pending
	demand := int32((int(slots) + perBrowser - 1) / perBrowser)
	demandSource := "claims"
	demandFallback := false
	switch {
	case s.cfg.DemandSource == "published" && s.cfg.DemandReader != nil:
		published, ok, err := s.cfg.DemandReader(ctx)
		switch {
		case err != nil:
			s.log.Printf("autoscaler: published demand unavailable: %v; sizing on claims", err)
			demandFallback = true
		case !ok:
			demandFallback = true
		default:
			contexts := published.Contexts
			if contexts < 1 {
				contexts = perBrowser
			}
			demand = int32((published.Live + contexts - 1) / contexts)
			demandSource = "published"
		}
	case s.cfg.DemandSource == "published":
		demandFallback = true
	}

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
		// Memory joins the pressure decision when it is read and configured: the
		// resource that stops this host is memory, and the CPU guard cannot see
		// it. A zero threshold disables that term, which is v1's behaviour.
		saturated := load.PressureFull > pressureHigh || load.Utilization > utilHigh ||
			(s.cfg.MemoryReserveMB > 0 && load.MemAvailableMB > 0 && load.MemAvailableMB < s.cfg.MemoryReserveMB) ||
			(s.cfg.MemPressureHigh > 0 && load.MemoryPressureFull > s.cfg.MemPressureHigh)
		free := load.PressureFull < pressureLow && load.Utilization < utilLow &&
			(s.cfg.MemoryReserveMB == 0 || load.MemAvailableMB == 0 || load.MemAvailableMB > s.cfg.MemoryReserveMB) &&
			(s.cfg.MemPressureHigh == 0 || load.MemoryPressureFull < s.cfg.MemPressureHigh)
		switch {
		case saturated:
			target = pool.Desired - 1 // relieve the node
		case free:
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
			MemAvailableMB: load.MemAvailableMB, MemoryPressureFull: load.MemoryPressureFull,
			ImageTag: s.cfg.ImageTag, MaxProvisioning: s.cfg.MaxProvisioning,
			DemandSource: demandSource, DemandPods: demand, DemandFallback: demandFallback,
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
		MemAvailableMB: load.MemAvailableMB, MemoryPressureFull: load.MemoryPressureFull,
		ImageTag: s.cfg.ImageTag, MaxProvisioning: s.cfg.MaxProvisioning,
		DemandSource: demandSource, DemandPods: demand, DemandFallback: demandFallback,
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
		s.log.Printf("scale %s: desired %d -> %d (pending %d, ready %d, demand %d from %s, max %d)",
			direction, pool.Desired, target, stats.Pending, stats.Ready, demand, demandSource, s.cfg.Max)
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
