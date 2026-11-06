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
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/logging"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/report"
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
	heavy := envInt("RIOT_HEAVY", 1)
	// RIOT_BOOT caps simultaneous startups: the CPU spike convergence knob.
	boot := envInt("RIOT_BOOT", 0)
	// Which cell the run lands in. Defaults are the CPU cell, so the historical
	// command measures what it always did; RIOT_POOL/RIOT_TEMPLATE point the same
	// workload at the GPU cell (browser-pool-gpu / browser-tpl-gpu).
	pool := envString("RIOT_POOL", "browser-pool")
	template := envString("RIOT_TEMPLATE", "browser-tpl")
	class := model.Class(envString("RIOT_CLASS", string(model.ClassNormal)))
	urls := riotURLsWithHeavy(sessions, heavy)

	cfg := model.RunConfig{
		RunID:     "riot-" + time.Now().UTC().Format("20060102t150405"),
		Namespace: "sandbox-demo", Pool: pool, Template: template,
		Policy: model.PolicyReuse, Class: class, FixedArrival: true, Rate: rate,
		ContextsPerBrowser: 8, CDPTransport: "ws", CDPPort: 9224, BootConcurrency: boot,
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
	// Keep the record: a run whose database lives in a temp directory leaves
	// nothing to inspect afterwards, which is exactly what went wrong the first
	// time. RIOT_OUT overrides the location.
	outDir := os.Getenv("RIOT_OUT")
	if outDir == "" {
		outDir = repoRunsDir(t)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("out dir %s: %v", outDir, err)
	}
	dbPath := filepath.Join(outDir, cfg.RunID+".db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	sampler, _ := measure.New(measure.Options{CgroupRoot: "/sys/fs/cgroup"})

	runner := session.New(cfg, session.Options{
		Adapter: adapter, Store: st, Plan: plan, Sampler: sampler,
		Log: log.New(os.Stderr, "riot: ", log.LstdFlags),
		// Info level so the boot lines (peak cores per instance) are visible: that
		// number is what the CPU study is about.
		Logger: logging.New(os.Stderr, logging.LevelInfo),
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

	// The database is written as the run goes (every transition, WAL), so it always
	// exists. The JSON is a snapshot built from it, and this is the one place that
	// writes it: called here, and again from a cleanup so a failing or panicking
	// test still leaves the file.
	writeReport := func() string {
		built, err := report.Build(cfg, plan, st)
		if err != nil {
			t.Logf("report build failed: %v", err)
			return ""
		}
		jsonPath := filepath.Join(outDir, cfg.RunID+".json")
		if err := built.Write(jsonPath); err != nil {
			t.Logf("report write failed: %v", err)
			return ""
		}
		built.Print(os.Stdout)
		return jsonPath
	}
	jsonPath := writeReport()
	t.Logf("database: %s", dbPath)
	t.Logf("report:   %s", jsonPath)
	t.Cleanup(func() {
		// Runs before the store closes (cleanups are LIFO): if the test aborted
		// before the line above, the report is still produced here.
		if jsonPath == "" {
			if path := writeReport(); path != "" {
				t.Logf("report (written on exit): %s", path)
			}
		}
	})

	t.Logf("workload: %d sessions at %.2f/s, hold %s, window ~%s, offered %.1f, heavy %d, boot concurrency %d",
		sessions, rate, hold, window.Round(time.Second), float64(sessions)/window.Seconds()*hold.Seconds(), heavy, boot)
	if len(rows) != sessions {
		t.Fatalf("%d rows for %d sessions", len(rows), sessions)
	}

	contexts := map[string]bool{}
	failed := 0
	// A control-plane blip (kine refusing writes) is a property of this cluster,
	// not of the browser layer. Those sessions are counted and reported, and only
	// a real failure ratio fails the test.
	transportFailed := 0
	var heavyTaskTotals, lightTaskTotals float64
	var heavyCount, lightCount int
	for _, row := range rows {
		if row.Outcome != model.OutcomeSuccess {
			failed++
			if transportFailure(row.Error) {
				transportFailed++
				t.Logf("session %d was refused by the control plane, not by the browser layer: %s",
					row.ID, row.Error)
				continue
			}
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
	// Every session must either succeed or be refused by the API server. Anything
	// else (a reset failure, a lost browser, a missing context) fails the run, as
	// does more than 2% refused.
	tolerated := sessions / 50
	if failed-transportFailed > 0 || transportFailed > tolerated {
		t.Fatalf("%d of %d sessions failed (%d of them control-plane refusals, tolerance %d); see above",
			failed, sessions, transportFailed, tolerated)
	}
	if transportFailed > 0 {
		t.Logf("%d of %d sessions were refused by the control plane; the browser layer served the rest",
			transportFailed, sessions)
	}
	if len(contexts) != sessions {
		t.Fatalf("%d sessions used %d contexts; each session needs its own", sessions, len(contexts))
	}

	// With at least one heavy page, it must cost more main-thread time than a
	// splash — otherwise nothing rendered. RIOT_HEAVY=0 is the all-splash density
	// run, where there is nothing to compare against.
	if heavyCount == 0 && lightCount == 0 {
		t.Fatalf("no task time recorded at all")
	}
	if heavyCount > 0 && lightCount > 0 {
		heavyAvg := heavyTaskTotals / float64(heavyCount)
		lightAvg := lightTaskTotals / float64(lightCount)
		t.Logf("task time: heavy %.1fms, light average %.1fms over %d sessions",
			heavyAvg/1000, lightAvg/1000, lightCount)
		if heavyAvg <= lightAvg {
			t.Fatalf("the heavy page cost %.1fms, no more than a splash at %.1fms: nothing rendered",
				heavyAvg/1000, lightAvg/1000)
		}
	} else {
		avg := lightTaskTotals / float64(lightCount+heavyCount)
		t.Logf("task time: %.1fms average over %d splash sessions", avg/1000, lightCount+heavyCount)
	}
	t.Logf("pages: %d distinct, first %s, last %s", len(urls), urls[0], urls[len(urls)-1])

	assertNoRiotLeaks(t, cfg)
}

// repoRunsDir finds <repo>/docs/session-lab-runs, the directory the script uses.
func repoRunsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for depth := 0; depth < 6; depth++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "..", "..", "docs", "session-lab-runs")
		}
		dir = filepath.Dir(dir)
	}
	return filepath.Join(os.TempDir(), "session-lab-runs")
}

// envString reads a string knob, falling back to a default.
func envString(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
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

// transportFailure reports whether a session failed because the API server refused
// it, rather than because the browser layer misbehaved.
func transportFailure(message string) bool {
	for _, marker := range []string{
		"connection refused", "unexpected eof", "apiserver not ready",
		"deadline exceeded", "temporarily unavailable", "server is currently unable",
	} {
		if strings.Contains(strings.ToLower(message), marker) {
			return true
		}
	}
	return false
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
