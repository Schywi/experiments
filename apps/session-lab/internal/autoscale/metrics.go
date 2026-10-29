package autoscale

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics exports what the autoscaler decides.
type Metrics struct {
	registry *prometheus.Registry

	target  prometheus.Gauge
	desired prometheus.Gauge
	ready   prometheus.Gauge
	pending prometheus.Gauge
	minG    prometheus.Gauge
	maxG    prometheus.Gauge
	changes *prometheus.CounterVec
	scaleUp *prometheus.HistogramVec

	mu sync.Mutex
}

// NewMetrics registers the autoscaler families.
func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()
	m := &Metrics{
		registry: registry,
		target: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "session_lab_autoscaler_target_replicas",
			Help: "Replicas the autoscaler computed from demand, after clamping.",
		}),
		desired: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "session_lab_autoscaler_pool_desired",
			Help: "Pool spec.replicas as the autoscaler last read it.",
		}),
		ready: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "session_lab_autoscaler_pool_ready",
			Help: "Pool ready replicas.",
		}),
		pending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "session_lab_autoscaler_pending_claims",
			Help: "Claims waiting for an instance: the demand signal.",
		}),
		minG: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "session_lab_autoscaler_min_replicas",
			Help: "Configured lower bound.",
		}),
		maxG: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "session_lab_autoscaler_max_replicas",
			Help: "Configured upper bound.",
		}),
		changes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "session_lab_autoscaler_changes_total",
			Help: "Replica changes by direction.",
		}, []string{"direction"}),
		scaleUp: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "session_lab_autoscaler_scale_out_seconds",
			Help:    "Time from a scale-out decision until the pool was ready and the queue empty.",
			Buckets: []float64{1, 2.5, 5, 10, 20, 30, 45, 60, 120, 300},
		}, []string{"pool"}),
	}
	registry.MustRegister(m.target, m.desired, m.ready, m.pending, m.minG, m.maxG, m.changes, m.scaleUp)
	return m
}

// Handler serves the registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Observe implements Recorder.
func (m *Metrics) Observe(settings Settings, changed bool, direction string) {
	m.target.Set(float64(settings.Target))
	m.desired.Set(float64(settings.Desired))
	m.ready.Set(float64(settings.Ready))
	m.pending.Set(float64(settings.Pending))
	m.minG.Set(float64(settings.Min))
	m.maxG.Set(float64(settings.Max))
	if changed && direction != "" {
		m.changes.WithLabelValues(direction).Inc()
	}
}

// ScaleOut implements Recorder.
func (m *Metrics) ScaleOut(seconds float64) {
	m.scaleUp.WithLabelValues("pool").Observe(seconds)
}

var _ Recorder = (*Metrics)(nil)
