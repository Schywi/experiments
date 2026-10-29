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
package autoscale

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/adapter"
)

// Config is one autoscaler's settings.
type Config struct {
	Namespace string
	Pool      string
	Min       int32
	Max       int32
	Interval  time.Duration
	// ConfigMap is where the settings are published so a run can record what
	// the autoscaler was configured to do.
	ConfigMap string
	Log       *log.Logger
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
	Desired   int32   `json:"desired"`
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
	ad      adapter.Adapter
	pub     Publisher
	rec     Recorder
	log     *log.Logger
	scaleUp *time.Time
}

// New builds a scaler.
func New(cfg Config, ad adapter.Adapter, pub Publisher, rec Recorder) *Scaler {
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
	target := stats.Ready + stats.Pending
	if target < s.cfg.Min {
		target = s.cfg.Min
	}
	if target > s.cfg.Max {
		target = s.cfg.Max
	}

	settings := Settings{
		Min: s.cfg.Min, Max: s.cfg.Max, IntervalS: s.cfg.Interval.Seconds(),
		Namespace: s.cfg.Namespace, Pool: s.cfg.Pool,
		UpdatedNS: time.Now().UnixNano(),
		Target:    target, Ready: stats.Ready, Pending: stats.Pending, Failed: stats.Failed,
		Desired: pool.Desired,
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
