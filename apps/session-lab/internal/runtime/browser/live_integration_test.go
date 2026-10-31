//go:build integration

package browser_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
)

// TestLiveBrowser exercises a real Chromium through the forwarding sidecar.
//
//	CDP_ADDR=<podIP>:9224 go test -tags integration ./internal/runtime/browser/ -run TestLive -v
//
// It is opt-in because it needs a pod: the unit tests above run against a fake
// that speaks the protocol, and this one proves the protocol against the browser
// that will actually serve sessions.
func TestLiveBrowser(t *testing.T) {
	addr := os.Getenv("CDP_ADDR")
	if addr == "" {
		t.Skip("CDP_ADDR not set; set it to <podIP>:9224 to run against a real browser")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := browser.Open(ctx, addr, nil)
	if err != nil {
		t.Fatalf("open %s: %v", addr, err)
	}
	defer conn.Close()

	baseline, err := conn.Targets(ctx)
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	t.Logf("browser at %s reports %d target(s) before the test", addr, len(baseline))

	// Two contexts at once: the whole point of sharing one browser.
	first, err := conn.NewContext(ctx)
	if err != nil {
		t.Fatalf("create context 1: %v", err)
	}
	second, err := conn.NewContext(ctx)
	if err != nil {
		t.Fatalf("create context 2: %v", err)
	}
	if first == second {
		t.Fatalf("two contexts share the id %q", first)
	}

	target1, err := conn.NewTarget(ctx, "about:blank", first)
	if err != nil {
		t.Fatalf("create target 1: %v", err)
	}
	target2, err := conn.NewTarget(ctx, "about:blank", second)
	if err != nil {
		t.Fatalf("create target 2: %v", err)
	}

	session, err := conn.Attach(ctx, target1)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := session.EnableMetrics(ctx); err != nil {
		t.Fatalf("enable metrics: %v", err)
	}
	if err := session.EnablePage(ctx); err != nil {
		t.Fatalf("enable page events: %v", err)
	}
	before, err := session.Metrics(ctx)
	if err != nil {
		t.Fatalf("metrics before: %v", err)
	}

	if _, err := session.Navigate(ctx, "https://example.com/"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if err := session.WaitForLoad(ctx); err != nil {
		t.Fatalf("wait for load: %v", err)
	}
	after, err := session.Metrics(ctx)
	if err != nil {
		t.Fatalf("metrics after: %v", err)
	}
	delta := after.Delta(before)
	t.Logf("session cost: task %.0fms script %.0fms heap %.0fKiB nodes %.0f",
		delta.TaskDurationSeconds*1000, delta.ScriptDurationSeconds*1000,
		delta.JSHeapUsedBytes/1024, delta.NodesCount)
	if delta.TaskDurationSeconds <= 0 {
		t.Fatalf("a loaded page reported %.3fs of task duration, want > 0",
			delta.TaskDurationSeconds)
	}

	targets, err := conn.Targets(ctx)
	if err != nil {
		t.Fatalf("targets after create: %v", err)
	}
	if len(targets) != len(baseline)+2 {
		t.Fatalf("%d targets after creating two pages, want %d", len(targets), len(baseline)+2)
	}

	// Cleanup must leave nothing behind, exactly as a session release does.
	if err := session.Close(ctx); err != nil {
		t.Fatalf("close session: %v", err)
	}
	for _, target := range []browser.TargetID{target1, target2} {
		if err := conn.CloseTarget(ctx, target); err != nil {
			t.Fatalf("close target %s: %v", target, err)
		}
	}
	for _, contextID := range []browser.ContextID{first, second} {
		if err := conn.DisposeContext(ctx, contextID); err != nil {
			t.Fatalf("dispose context %s: %v", contextID, err)
		}
	}
	final, err := conn.Targets(ctx)
	if err != nil {
		t.Fatalf("targets after cleanup: %v", err)
	}
	if len(final) != len(baseline) {
		t.Fatalf("cleanup left %d targets, want the baseline %d", len(final), len(baseline))
	}
}
