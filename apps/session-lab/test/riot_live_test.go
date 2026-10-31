//go:build integration

package test

// The riot experiment, as one Go test. No script, no flag, no 40 KB argument:
// the URLs are handed straight to the runner, which treats them as an opaque
// per-session list.
//
//	SESSION_LAB_INTEGRATION=1 go test -tags integration -count=1 -timeout 900s \
//	  ./test/ -run TestRiotLive -v

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/kube"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/measure"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/session"
)

func TestRiotLive(t *testing.T) {
	if os.Getenv("SESSION_LAB_INTEGRATION") != "1" {
		t.Skip("set SESSION_LAB_INTEGRATION=1 to run against the cluster")
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	}
	if _, err := os.Stat(kubeconfig); err != nil {
		t.Skipf("no kubeconfig at %s", kubeconfig)
	}

	// Defaults keep a plain `go test -run TestRiotLive` short. The three knobs turn
	// it into a soak without touching this file:
	//   RIOT_SESSIONS=300 RIOT_HOLD=75s RIOT_RATE=0.5  -> ten-minute window
	sessions := envInt("RIOT_SESSIONS", len(riotChampions))
	hold := envDuration("RIOT_HOLD", 20*time.Second)
	rate := envFloat("RIOT_RATE", 2)
	urls := riotURLs(sessions)

	cfg := model.RunConfig{
		RunID:     "riot-" + time.Now().UTC().Format("20060102t150405"),
		Namespace: "sandbox-demo", Pool: "browser-pool", Template: "browser-tpl",
		Policy: model.PolicyReuse, Class: model.ClassNormal, FixedArrival: true, Rate: rate,
		ContextsPerBrowser: 8, CDPTransport: "ws", CDPPort: 9224,
		TargetURLs: urls, SampleSeconds: 1, Sessions: sessions,
		Mix: []model.Bucket{{Label: hold.String(), Hold: hold, Count: sessions}},
	}
	plan, err := schedule.Generate(cfg, 1)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	adapter, err := kube.NewKube(kubeconfig, "")
	if err != nil {
		t.Fatalf("kube adapter: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "riot.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	sampler, _ := measure.New(measure.Options{CgroupRoot: "/sys/fs/cgroup"})

	runner := session.New(cfg, session.Options{
		Adapter: adapter, Store: st, Plan: plan, Sampler: sampler,
		Log: log.New(os.Stderr, "riot: ", log.LstdFlags),
	})
	// Bound the run by what it is meant to take, not by a constant: a ten-minute
	// window plus one hold plus teardown.
	window := time.Duration(float64(sessions)/rate) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), window+hold+5*time.Minute)
	defer cancel()
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	t.Logf("workload: %d sessions at %.2f/s, hold %s, window ~%s, offered %.1f",
		sessions, rate, hold, window.Round(time.Second), float64(sessions)/window.Seconds()*hold.Seconds())
	if len(rows) != sessions {
		t.Fatalf("%d rows for %d sessions", len(rows), sessions)
	}

	contexts := map[string]bool{}
	failed := 0
	var heavyTaskTotals, lightTaskTotals float64
	var heavyCount, lightCount int
	for _, row := range rows {
		if row.Outcome != model.OutcomeSuccess {
			failed++
			t.Logf("session %d ended %q: %s", row.ID, row.Outcome, row.Error)
			continue
		}
		if row.BrowserContext == "" {
			t.Fatalf("session %d ran without its own browser context", row.ID)
		}
		contexts[row.BrowserContext] = true
		if row.ID == 1 {
			heavyTaskTotals += float64(row.TaskDurationUS)
			heavyCount++
		} else {
			lightTaskTotals += float64(row.TaskDurationUS)
			lightCount++
		}
	}
	if failed > 0 {
		t.Fatalf("%d of %d sessions failed; see the lines above", failed, sessions)
	}
	if len(contexts) != sessions {
		t.Fatalf("%d sessions used %d contexts; each session needs its own", sessions, len(contexts))
	}

	// The heavy page is the point: a real SPA must cost more main-thread time than
	// a splash image, otherwise the session is not rendering anything.
	if heavyCount == 0 || lightCount == 0 {
		t.Fatalf("no task time recorded (heavy %d, light %d)", heavyCount, lightCount)
	}
	heavyAvg := heavyTaskTotals / float64(heavyCount)
	lightAvg := lightTaskTotals / float64(lightCount)
	t.Logf("task time: heavy %.1fms, light average %.1fms over %d sessions",
		heavyAvg/1000, lightAvg/1000, lightCount)
	t.Logf("pages: %d distinct, first %s, last %s", len(urls), urls[0], urls[len(urls)-1])
	if heavyAvg <= lightAvg {
		t.Fatalf("the heavy page cost %.1fms, no more than a splash at %.1fms: nothing rendered",
			heavyAvg/1000, lightAvg/1000)
	}

	assertNoRiotLeaks(t, cfg)
}

// envInt reads an integer knob, falling back to a default.
func envInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

// envFloat reads a float knob.
func envFloat(key string, fallback float64) float64 {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

// envDuration reads a duration knob such as 75s or 5m.
func envDuration(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func assertNoRiotLeaks(t *testing.T, cfg model.RunConfig) {
	t.Helper()
	out, err := exec.Command("kubectl", "-n", cfg.Namespace, "get", "sandboxclaims", "-o", "name").CombinedOutput()
	if err != nil {
		t.Skipf("kubectl unavailable: %v", err)
	}
	prefix := "lab-" + cfg.RunID + "-"
	if index := strings.Index(string(out), prefix); index >= 0 {
		t.Fatalf("claims from %s are still present:\n%s", cfg.RunID, out)
	}
}
