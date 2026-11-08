//go:build integration

package persist

// The isolated round trip, as one test and nothing else:
//
//	spin a session, write identifiable state, capture it, destroy the session
//	completely, start a new one, restore, and read the state back.
//
// It needs a cluster and nothing else: no store, no ledger, no budget. The blob
// is a file in the test's own temporary directory, and the assertion is the
// value the page reads out of its own storage.
//
//	PERSIST_INTEGRATION=1 go test -tags integration -count=1 -timeout 900s \
//	  ./internal/persist/ -run TestRoundTrip -v
//
// PERSIST_TARGET_URL must be an http(s) page: a data: URL has an opaque origin,
// so it has no localStorage and would prove nothing.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/kube"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
)

const (
	// probeKey is written into localStorage and mirrored into a cookie, so both
	// surfaces are checked by name.
	probeKey    = "persist-probe"
	probeValue  = "v1"
	probeCookie = "persist_probe"

	// destroyAllowance is how long the old claim gets to disappear. A claim is
	// deleted on release and its sandbox goes with it; until that finishes, the
	// name is still taken and the second acquisition is retried.
	destroyAllowance = 3 * time.Minute
)

func TestRoundTrip(t *testing.T) {
	if os.Getenv("PERSIST_INTEGRATION") != "1" {
		t.Skip("set PERSIST_INTEGRATION=1 to run against the cluster")
	}
	kubeconfig := envOr("KUBECONFIG", filepath.Join(os.Getenv("HOME"), ".kube", "config"))
	if _, err := os.Stat(kubeconfig); err != nil {
		t.Skipf("no kubeconfig at %s", kubeconfig)
	}
	namespace := envOr("PERSIST_NAMESPACE", "sandbox-demo")
	pool := envOr("PERSIST_POOL", "browser-pool")
	target := envOr("PERSIST_TARGET_URL", "https://example.com/")
	port := envInt("PERSIST_CDP_PORT", 9224)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	adapter, err := kube.NewKube(kubeconfig, "")
	if err != nil {
		t.Fatalf("kube adapter: %v", err)
	}
	name := fmt.Sprintf("persist-rt-%d", time.Now().Unix()%100000)

	// 1. The original session: state, capture, then destroy everything.
	first := acquire(t, ctx, adapter, namespace, pool, name)
	t.Cleanup(func() { _ = adapter.Release(context.Background(), first) })
	conn, session, contextID := openPage(t, ctx, first, port, target)

	writeState(t, ctx, session)
	blob, err := Snapshot(ctx, conn, session, contextID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	raw, err := Encode(blob)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	blobPath := filepath.Join(t.TempDir(), "blob.json.gz")
	if err := os.WriteFile(blobPath, raw, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	t.Logf("captured %d bytes: %d cookies, %d localStorage items, origin %s",
		len(raw), len(blob.Cookies), blob.Items(), blob.Origin)
	if len(blob.Cookies) == 0 || blob.Items() == 0 {
		t.Fatalf("captured nothing to restore: %d cookies, %d items", len(blob.Cookies), blob.Items())
	}
	closePage(t, ctx, conn, session, contextID)
	if err := adapter.Release(ctx, first); err != nil {
		t.Fatalf("release: %v", err)
	}

	// 2. A new session on the same claim name. Acquire only succeeds once the
	// old claim is gone, so a successful acquisition is also the proof that the
	// first session was destroyed.
	second := reacquire(t, ctx, adapter, namespace, pool, name)
	t.Cleanup(func() { _ = adapter.Release(context.Background(), second) })
	conn2, session2, contextID2 := openPage(t, ctx, second, port, target)

	stored, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	restored, err := Decode(stored)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := Restore(ctx, conn2, session2, contextID2, restored); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// 3. The page reads its own state back.
	if _, err := session2.Navigate(ctx, target); err != nil {
		t.Fatalf("navigate after restore: %v", err)
	}
	if err := session2.WaitForLoad(ctx); err != nil {
		t.Fatalf("load after restore: %v", err)
	}
	value := evaluate(t, ctx, session2, fmt.Sprintf("localStorage.getItem(%q)", probeKey))
	if value != probeValue {
		t.Errorf("localStorage %s is %q, want %q", probeKey, value, probeValue)
	}
	cookies := evaluate(t, ctx, session2, "document.cookie")
	if !containsCookie(cookies, probeCookie, probeValue) {
		t.Errorf("document.cookie %q does not carry %s=%s", cookies, probeCookie, probeValue)
	}
}

// reacquire retries the same claim name until it is free, which it becomes when
// the previous claim and its sandbox are gone.
func reacquire(t *testing.T, ctx context.Context, adapter *kube.Kube, namespace, pool, name string) *sandbox.Instance {
	t.Helper()
	deadline := time.Now().Add(destroyAllowance)
	var lastErr error
	for time.Now().Before(deadline) {
		instance, err := adapter.Acquire(ctx, namespace, pool, name, model.ClassNormal)
		if err == nil {
			if err := adapter.WaitReady(ctx, instance); err != nil {
				t.Fatalf("wait ready: %v", err)
			}
			return instance
		}
		lastErr = err
		select {
		case <-ctx.Done():
			t.Fatalf("reacquire %s: %v", name, ctx.Err())
		case <-time.After(time.Second):
		}
	}
	t.Fatalf("reacquire %s after %s: %v", name, destroyAllowance, lastErr)
	return nil
}

// acquire creates the claim and waits for a usable browser.
func acquire(t *testing.T, ctx context.Context, adapter *kube.Kube, namespace, pool, name string) *sandbox.Instance {
	t.Helper()
	instance, err := adapter.Acquire(ctx, namespace, pool, name, model.ClassNormal)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := adapter.WaitReady(ctx, instance); err != nil {
		t.Fatalf("wait ready: %v", err)
	}
	if instance.PodIP == "" {
		t.Fatalf("instance %s has no pod IP", instance.ID)
	}
	return instance
}

// openPage dials the browser, creates a context and a page, and loads the target
// so the context has an origin to store state under.
func openPage(t *testing.T, ctx context.Context, instance *sandbox.Instance, port int, url string) (*browser.Conn, *browser.Session, browser.ContextID) {
	t.Helper()
	conn, err := browser.Open(ctx, fmt.Sprintf("%s:%d", instance.PodIP, port), nil)
	if err != nil {
		t.Fatalf("open %s: %v", instance.PodIP, err)
	}
	contextID, err := conn.NewContext(ctx)
	if err != nil {
		conn.Close()
		t.Fatalf("new context: %v", err)
	}
	targetID, err := conn.NewTarget(ctx, url, contextID)
	if err != nil {
		conn.Close()
		t.Fatalf("new target: %v", err)
	}
	session, err := conn.Attach(ctx, targetID)
	if err != nil {
		conn.Close()
		t.Fatalf("attach: %v", err)
	}
	if err := session.EnablePage(ctx); err != nil {
		t.Fatalf("enable page: %v", err)
	}
	if _, err := session.Navigate(ctx, url); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if err := session.WaitForLoad(ctx); err != nil {
		t.Fatalf("load: %v", err)
	}
	return conn, session, contextID
}

// closePage is the destroy: session, page, context, connection.
func closePage(t *testing.T, ctx context.Context, conn *browser.Conn, session *browser.Session, contextID browser.ContextID) {
	t.Helper()
	if session != nil {
		_ = session.Close(ctx)
	}
	if targets, err := conn.Targets(ctx); err == nil {
		for _, id := range targets {
			_ = conn.CloseTarget(ctx, id)
		}
	}
	_ = conn.DisposeContext(ctx, contextID)
	_ = conn.Close()
}

// writeState puts one identifiable value into localStorage and one cookie, so
// both surfaces have something to survive.
func writeState(t *testing.T, ctx context.Context, session *browser.Session) {
	t.Helper()
	script := fmt.Sprintf("localStorage.setItem(%q, %q); document.cookie = %q;",
		probeKey, probeValue, probeCookie+"="+probeValue+"; path=/")
	if _, err := session.Do(ctx, "Runtime.evaluate", map[string]any{
		"expression":    script,
		"returnByValue": true,
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if got := evaluate(t, ctx, session, fmt.Sprintf("localStorage.getItem(%q)", probeKey)); got != probeValue {
		t.Fatalf("the state did not stick before capture: %s is %q", probeKey, got)
	}
}

// evaluate runs a script in the page and returns its string result.
func evaluate(t *testing.T, ctx context.Context, session *browser.Session, expression string) string {
	t.Helper()
	value, err := evalString(ctx, session, expression)
	if err != nil {
		t.Fatalf("evaluate %s: %v", expression, err)
	}
	return value
}

// containsCookie reports whether a document.cookie string carries name=value.
func containsCookie(header, name, value string) bool {
	want := name + "=" + value
	for _, part := range splitCookieHeader(header) {
		if part == want {
			return true
		}
	}
	return false
}

func splitCookieHeader(header string) []string {
	var out []string
	start := 0
	for i := 0; i < len(header); i++ {
		if header[i] != ';' {
			continue
		}
		out = append(out, trimSpace(header[start:i]))
		start = i + 1
	}
	out = append(out, trimSpace(header[start:]))
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
