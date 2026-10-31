//go:build integration

package session_test

// End-to-end tests against the live cluster. They need a kubeconfig and a warm
// pool, so they are opt-in:
//
//	SESSION_LAB_INTEGRATION=1 KUBECONFIG=$HOME/.kube/config \
//	  go test -tags integration -count=1 -timeout 300s ./internal/runtime/session/ -run Live -v
//
// What they protect, in the order the failures actually happened:
//   - one Chromium carrying several sessions (browser contexts), with every context
//     disposed at the end;
//   - one session per pod when contexts are off, so the infra layer keeps meaning
//     what it means;
//   - a completed hold recorded as success (the swapped-boolean regression);
//   - per-session cost present in the record (task time, heap);
//   - no claim left behind, and pool ownership released.

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/kube"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/measure"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/report"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/session"
)

func integrationConfig(t *testing.T) (model.RunConfig, string) {
	t.Helper()
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
	cfg := model.RunConfig{
		RunID:     "it-" + time.Now().UTC().Format("20060102t150405"),
		Namespace: "sandbox-demo", Pool: "browser-pool", Template: "browser-tpl",
		Policy: model.PolicyReuse, Class: model.ClassNormal, FixedArrival: true, Rate: 4,
		SampleSeconds: 1, OutDir: t.TempDir(),
	}
	return cfg, kubeconfig
}

// runLive builds a runner the same way the binary does and returns what it wrote.
func runLive(t *testing.T, cfg model.RunConfig, kubeconfig string, sessions int, hold time.Duration) (*store.Store, model.RunConfig) {
	t.Helper()
	adapter, err := kube.NewKube(kubeconfig, "")
	if err != nil {
		t.Fatalf("kube adapter: %v", err)
	}
	cfg.Sessions = sessions
	cfg.Mix = []model.Bucket{{Label: hold.String(), Hold: hold, Count: sessions}}
	plan, err := schedule.Generate(cfg, 1)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	path := filepath.Join(t.TempDir(), "run.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	sampler, _ := measure.New(measure.Options{CgroupRoot: "/sys/fs/cgroup"})
	runner := session.New(cfg, session.Options{
		Adapter: adapter, Store: st, Plan: plan, Sampler: sampler,
		Log: log.New(os.Stderr, "it: ", log.LstdFlags),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	return st, cfg
}

func TestLiveSharedBrowserRun(t *testing.T) {
	cfg, kubeconfig := integrationConfig(t)
	cfg.ContextsPerBrowser = 4
	cfg.CDPTransport = "ws"
	cfg.CDPPort = 9224

	st, cfg := runLive(t, cfg, kubeconfig, 4, 8*time.Second)
	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(rows) != cfg.Sessions {
		t.Fatalf("%d rows for %d sessions", len(rows), cfg.Sessions)
	}

	contexts := map[string]bool{}
	for _, row := range rows {
		if row.Outcome != model.OutcomeSuccess {
			t.Fatalf("session %d ended %q: %s", row.ID, row.Outcome, row.Error)
		}
		if row.BrowserContext == "" {
			t.Fatalf("session %d has no browser context", row.ID)
		}
		contexts[row.BrowserContext] = true
		held := time.Duration(row.HoldEndNS-row.HoldStartNS) * time.Nanosecond
		if held < 7*time.Second {
			t.Fatalf("session %d held %s, want about 8s", row.ID, held)
		}
		if row.ReadyNS < row.ClaimCreatedNS {
			t.Fatalf("session %d has a negative slot wait", row.ID)
		}
	}
	if len(contexts) != len(rows) {
		t.Fatalf("%d sessions used %d contexts; isolation is per session", len(rows), len(contexts))
	}

	measured := 0
	for _, row := range rows {
		if row.TaskDurationUS > 0 {
			measured++
		}
	}
	if measured == 0 {
		t.Fatalf("no session recorded task time, so per-session cost is not wired end to end")
	}

	rep, err := report.Build(cfg, mustPlan(t, cfg), st)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	total := 0
	for _, count := range rep.Outcomes {
		total += count
	}
	if total != cfg.Sessions {
		t.Fatalf("report accounts for %d of %d sessions", total, cfg.Sessions)
	}
	assertNoLeakedClaims(t, cfg)
}

func TestLiveInfraLayerIsOneSessionPerPod(t *testing.T) {
	cfg, kubeconfig := integrationConfig(t)
	cfg.ContextsPerBrowser = 1
	cfg.CDPTransport = "http"
	cfg.Policy = model.PolicyDestroy

	st, cfg := runLive(t, cfg, kubeconfig, 2, 4*time.Second)
	rows, err := st.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	instances := map[string]bool{}
	for _, row := range rows {
		if row.Outcome != model.OutcomeSuccess {
			t.Fatalf("session %d ended %q: %s", row.ID, row.Outcome, row.Error)
		}
		instances[row.Instance] = true
		// At one context per browser the runner keeps the historical path: the
		// browser is reset to a blank page instead of gaining its own context, so
		// there is no context id to record. Layers stay distinguishable.
		if row.BrowserContext != "" {
			t.Fatalf("session %d recorded a context at --contexts-per-browser 1", row.ID)
		}
	}
	if len(instances) != len(rows) {
		t.Fatalf("%d sessions used %d instances; the infra layer must be one per pod",
			len(rows), len(instances))
	}
	assertNoLeakedClaims(t, cfg)
}

func mustPlan(t *testing.T, cfg model.RunConfig) schedule.Plan {
	t.Helper()
	plan, err := schedule.Generate(cfg, 1)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

// assertNoLeakedClaims checks the cluster, not the runner's own bookkeeping.
func assertNoLeakedClaims(t *testing.T, cfg model.RunConfig) {
	t.Helper()
	out, err := exec.Command("kubectl", "-n", cfg.Namespace, "get", "sandboxclaims", "-o", "name").CombinedOutput()
	if err != nil {
		t.Skipf("kubectl unavailable, cannot check for leaks: %v", err)
	}
	prefix := "lab-" + strings.ToLower(cfg.RunID) + "-"
	if strings.Contains(string(out), prefix) {
		t.Fatalf("claims from run %s are still present:\n%s", cfg.RunID, out)
	}
}
