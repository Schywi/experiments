package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/run"
)

func scrape(t *testing.T, r *Recorder) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	r.Handler().ServeHTTP(rec, req)
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

func TestRecorderExportsLifecycleSeries(t *testing.T) {
	r := New()

	r.SessionArrived()
	r.Pool(4, 3)
	r.SessionAcquired()
	r.QueueWait("reuse", 0.103)
	r.Acquire("reuse", "warm", 0.106)
	r.Hold("reuse", "5s", 5.02)
	r.Reset("reuse", 0.007)
	r.Resources("reuse", run.Reading{CPUUsec: 1_190_000, ThrottledUsec: 250_000, NRThrottled: 7,
		MemWorkingSet: 148 << 20, OOMKill: 0})
	r.Boundary("session_cpu_throttled")
	r.SessionSettled("reuse", model.OutcomeSuccess)

	body := scrape(t, r)
	for _, want := range []string{
		"session_lab_sessions_active 0",
		"session_lab_sessions_waiting 0",
		`session_lab_sessions_total{outcome="success",policy="reuse"} 1`,
		"session_lab_queue_wait_seconds_count{policy=\"reuse\"} 1",
		`session_lab_acquire_seconds_count{launch_type="warm",policy="reuse"} 1`,
		`session_lab_hold_seconds_count{bucket="5s",policy="reuse"} 1`,
		"session_lab_reset_seconds_count{policy=\"reuse\"} 1",
		"session_lab_cpu_seconds_total{policy=\"reuse\"} 1.19",
		"session_lab_throttled_seconds_total{policy=\"reuse\"} 0.25",
		`session_lab_boundaries_total{kind="session_cpu_throttled"} 1`,
		"session_lab_pool_desired 4",
		"session_lab_pool_ready 3",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output is missing %q", want)
		}
	}
}

func TestLabelsStayLowCardinality(t *testing.T) {
	r := New()
	// Fifty sessions, one label set: the series count must not grow with the
	// number of sessions.
	for i := 0; i < 50; i++ {
		r.SessionArrived()
		r.QueueWait("destroy", 0.2)
		r.SessionSettled("destroy", model.OutcomeSuccess)
	}
	body := scrape(t, r)
	if got := strings.Count(body, "session_lab_queue_wait_seconds_count"); got != 1 {
		t.Fatalf("queue wait grew %d series for one policy", got)
	}
	if strings.Contains(body, "session=") || strings.Contains(body, "pod_uid=") {
		t.Fatalf("session identity leaked into a label:\n%s", body)
	}
}

func TestGaugesDoNotGoNegative(t *testing.T) {
	r := New()
	// Settling twice without an arrival must not drive the gauge below zero.
	r.SessionSettled("reuse", model.OutcomeWaiting)
	body := scrape(t, r)
	if !strings.Contains(body, "session_lab_sessions_active 0") {
		t.Fatalf("active gauge is not zero:\n%s", body)
	}
}
