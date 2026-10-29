// Package metrics exports what a run is doing to Prometheus.
//
// Label discipline: only values with a small fixed set appear as labels (policy,
// bucket, outcome, launch_type, boundary kind). Session identity, pod UID and
// sandbox name stay in SQLite, where they belong.
package metrics

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/run"
)

// Recorder holds the metric families and implements run.Recorder.
type Recorder struct {
	registry *prometheus.Registry

	active   prometheus.Gauge
	waiting  prometheus.Gauge
	sessions *prometheus.CounterVec
	queue    *prometheus.HistogramVec
	acquire  *prometheus.HistogramVec
	hold     *prometheus.HistogramVec
	reset    *prometheus.HistogramVec
	release  *prometheus.HistogramVec
	cpu      *prometheus.CounterVec
	throttle *prometheus.CounterVec
	oom      *prometheus.CounterVec
	workSet  *prometheus.HistogramVec
	poolWant prometheus.Gauge
	poolHave prometheus.Gauge
	boundary *prometheus.CounterVec

	// The gauges are also tracked here so decrementing them cannot go negative
	// when a session settles without ever having arrived through this recorder.
	mu      sync.Mutex
	activeN int64
	waitN   int64
}

// New registers the families on a fresh registry.
func New() *Recorder {
	registry := prometheus.NewRegistry()
	r := &Recorder{
		registry: registry,
		active: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "session_lab_sessions_active",
			Help: "Sessions holding or acquiring an instance right now.",
		}),
		waiting: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "session_lab_sessions_waiting",
			Help: "Sessions dispatched but not yet holding an instance.",
		}),
		sessions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "session_lab_sessions_total",
			Help: "Sessions that reached a terminal outcome, by outcome.",
		}, []string{"policy", "outcome"}),
		queue: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "session_lab_queue_wait_seconds",
			Help:    "Claim create to Ready, the time a session waited for an instance.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"policy"}),
		acquire: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "session_lab_acquire_seconds",
			Help:    "Arrival to Ready, the full cost of getting an instance.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"policy", "launch_type"}),
		hold: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "session_lab_hold_seconds",
			Help:    "Measured session hold duration.",
			Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600, 1200},
		}, []string{"policy", "bucket"}),
		reset: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "session_lab_reset_seconds",
			Help:    "Blank-page reset between sessions on a reused instance.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 5},
		}, []string{"policy"}),
		release: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "session_lab_release_seconds",
			Help:    "Delete call to instance gone.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"policy"}),
		cpu: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "session_lab_cpu_seconds_total",
			Help: "CPU seconds charged to sessions, from cpu.stat usage_usec.",
		}, []string{"policy"}),
		throttle: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "session_lab_throttled_seconds_total",
			Help: "CPU throttled time charged to sessions, from cpu.stat throttled_usec.",
		}, []string{"policy"}),
		oom: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "session_lab_oom_kills_total",
			Help: "OOM kills observed on session pods.",
		}, []string{"policy"}),
		workSet: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "session_lab_workingset_peak_bytes",
			Help:    "Peak working set per session.",
			Buckets: []float64{64 << 20, 128 << 20, 256 << 20, 512 << 20, 1 << 30, 2 << 30, 4 << 30},
		}, []string{"policy"}),
		poolWant: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "session_lab_pool_desired",
			Help: "Warm pool spec.replicas as reported during the run.",
		}),
		poolHave: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "session_lab_pool_ready",
			Help: "Warm pool ready replicas as reported during the run.",
		}),
		boundary: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "session_lab_boundaries_total",
			Help: "Times the platform or the node refused, throttled or reclaimed, by kind.",
		}, []string{"kind"}),
	}
	registry.MustRegister(r.active, r.waiting, r.sessions, r.queue, r.acquire, r.hold,
		r.reset, r.release, r.cpu, r.throttle, r.oom, r.workSet, r.poolWant, r.poolHave, r.boundary)
	return r
}

// Handler serves the registry.
func (r *Recorder) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

// SessionArrived implements run.Recorder.
func (r *Recorder) SessionArrived() {
	r.mu.Lock()
	r.activeN++
	r.waitN++
	r.active.Set(float64(r.activeN))
	r.waiting.Set(float64(r.waitN))
	r.mu.Unlock()
}

// SessionAcquired implements run.Recorder: the session stopped waiting.
func (r *Recorder) SessionAcquired() {
	r.mu.Lock()
	if r.waitN > 0 {
		r.waitN--
	}
	r.waiting.Set(float64(r.waitN))
	r.mu.Unlock()
}

// SessionSettled implements run.Recorder.
func (r *Recorder) SessionSettled(policy string, outcome model.Outcome) {
	r.mu.Lock()
	if r.activeN > 0 {
		r.activeN--
	}
	r.active.Set(float64(r.activeN))
	r.mu.Unlock()
	r.sessions.WithLabelValues(policy, string(outcome)).Inc()
}

// QueueWait implements run.Recorder.
func (r *Recorder) QueueWait(policy string, seconds float64) {
	r.queue.WithLabelValues(policy).Observe(seconds)
}

// Acquire implements run.Recorder.
func (r *Recorder) Acquire(policy, launchType string, seconds float64) {
	if launchType == "" {
		launchType = "unknown"
	}
	r.acquire.WithLabelValues(policy, launchType).Observe(seconds)
}

// Hold implements run.Recorder.
func (r *Recorder) Hold(policy, bucket string, seconds float64) {
	r.hold.WithLabelValues(policy, bucket).Observe(seconds)
}

// Reset implements run.Recorder.
func (r *Recorder) Reset(policy string, seconds float64) {
	r.reset.WithLabelValues(policy).Observe(seconds)
}

// Release implements run.Recorder.
func (r *Recorder) Release(policy string, seconds float64) {
	r.release.WithLabelValues(policy).Observe(seconds)
}

// Resources implements run.Recorder.
func (r *Recorder) Resources(policy string, reading run.Reading) {
	r.cpu.WithLabelValues(policy).Add(float64(reading.CPUUsec) / 1e6)
	if reading.ThrottledUsec > 0 {
		r.throttle.WithLabelValues(policy).Add(float64(reading.ThrottledUsec) / 1e6)
	}
	if reading.OOMKill > 0 {
		r.oom.WithLabelValues(policy).Add(float64(reading.OOMKill))
	}
	if reading.MemWorkingSet > 0 {
		r.workSet.WithLabelValues(policy).Observe(float64(reading.MemWorkingSet))
	}
}

// Pool implements run.Recorder.
func (r *Recorder) Pool(desired, ready int32) {
	r.poolWant.Set(float64(desired))
	r.poolHave.Set(float64(ready))
}

// Boundary implements run.Recorder.
func (r *Recorder) Boundary(kind string) {
	if kind == "" {
		kind = "unknown"
	}
	r.boundary.WithLabelValues(kind).Inc()
}

var _ run.Recorder = (*Recorder)(nil)
