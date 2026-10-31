package browser_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
)

// fakeBrowser speaks enough of the DevTools protocol to exercise the client: it
// answers on a real WebSocket, records what was asked, and can be told to refuse.
type fakeBrowser struct {
	server *httptest.Server

	mu       sync.Mutex
	contexts map[string][]string // context id -> target ids
	targets  []string
	closed   bool
	refuse   bool
	seen     []string
	nextID   int
}

func newFakeBrowser(t *testing.T) *fakeBrowser {
	t.Helper()
	fake := &fakeBrowser{contexts: map[string][]string{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		url := "ws" + strings.TrimPrefix(fake.server.URL, "http") + "/devtools/browser/test"
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"Browser":"Chrome/130.0.0.0","webSocketDebuggerUrl":%q}`, url)
	})
	mux.HandleFunc("/devtools/browser/test", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		fake.serve(conn)
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeBrowser) serve(conn *websocket.Conn) {
	ctx := context.Background()
	for {
		_, body, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var command struct {
			ID        int64          `json:"id"`
			Method    string         `json:"method"`
			Params    map[string]any `json:"params"`
			SessionID string         `json:"sessionId"`
		}
		if err := json.Unmarshal(body, &command); err != nil {
			continue
		}

		f.mu.Lock()
		f.seen = append(f.seen, command.Method)
		refuse := f.refuse
		f.mu.Unlock()

		reply := func(result any) {
			payload := map[string]any{"id": command.ID, "result": result}
			if command.SessionID != "" {
				payload["sessionId"] = command.SessionID
			}
			out, _ := json.Marshal(payload)
			conn.Write(ctx, websocket.MessageText, out)
		}
		failure := func(code int, message string) {
			out, _ := json.Marshal(map[string]any{
				"id": command.ID, "error": map[string]any{"code": code, "message": message},
			})
			conn.Write(ctx, websocket.MessageText, out)
		}

		switch command.Method {
		case "Target.createBrowserContext":
			if refuse {
				// Exactly what Chromium answers to a page-socket caller.
				failure(-32000, "Not allowed")
				continue
			}
			f.mu.Lock()
			f.nextID++
			id := fmt.Sprintf("ctx-%d", f.nextID)
			f.contexts[id] = nil
			f.mu.Unlock()
			reply(map[string]any{"browserContextId": id})
		case "Target.createTarget":
			f.mu.Lock()
			f.nextID++
			id := fmt.Sprintf("tgt-%d", f.nextID)
			f.targets = append(f.targets, id)
			if contextID, ok := command.Params["browserContextId"].(string); ok {
				f.contexts[contextID] = append(f.contexts[contextID], id)
			}
			f.mu.Unlock()
			reply(map[string]any{"targetId": id})
		case "Target.attachToTarget":
			reply(map[string]any{"sessionId": "sess-" + fmt.Sprint(command.Params["targetId"])})
		case "Target.getTargets":
			f.mu.Lock()
			infos := make([]map[string]any, 0, len(f.targets))
			for _, id := range f.targets {
				infos = append(infos, map[string]any{"targetId": id, "type": "page"})
			}
			f.mu.Unlock()
			reply(map[string]any{"targetInfos": infos})
		case "Target.closeTarget":
			f.mu.Lock()
			targetID, _ := command.Params["targetId"].(string)
			kept := f.targets[:0]
			for _, id := range f.targets {
				if id != targetID {
					kept = append(kept, id)
				}
			}
			f.targets = kept
			f.mu.Unlock()
			reply(map[string]any{"success": true})
		case "Target.disposeBrowserContext":
			id, _ := command.Params["browserContextId"].(string)
			f.mu.Lock()
			_, known := f.contexts[id]
			delete(f.contexts, id)
			f.mu.Unlock()
			if !known {
				failure(-32000, "Failed to find context with id "+id)
				continue
			}
			reply(map[string]any{})
		case "Target.detachFromTarget":
			reply(map[string]any{})
		case "Page.enable":
			reply(map[string]any{})
		case "Performance.enable":
			reply(map[string]any{})
		case "Performance.getMetrics":
			reply(map[string]any{"metrics": []map[string]any{
				{"name": "TaskDuration", "value": 1.5},
				{"name": "ScriptDuration", "value": 0.5},
				{"name": "JSHeapUsedSize", "value": 1048576},
				{"name": "Nodes", "value": 120},
				{"name": "LayoutCount", "value": 4},
			}})
		case "Page.navigate":
			reply(map[string]any{"frameId": "frame-1", "loaderId": "loader-1"})
			// The load event arrives on the session, after the navigation reply.
			go func(sessionID string) {
				time.Sleep(5 * time.Millisecond)
				out, _ := json.Marshal(map[string]any{
					"method": "Page.loadEventFired", "sessionId": sessionID,
					"params": map[string]any{"timestamp": 1},
				})
				conn.Write(context.Background(), websocket.MessageText, out)
			}(command.SessionID)
		default:
			failure(-32601, "'"+command.Method+"' wasn't found")
		}
	}
}

// push sends a browser-level event, the way Chromium reports a renderer crash.
func (f *fakeBrowser) push(targetID string) {
	// The fake keeps no persistent handle; crashes are exercised through the
	// dedicated fake in the test that needs them.
}

func (f *fakeBrowser) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.seen))
	copy(out, f.seen)
	return out
}

func open(t *testing.T, fake *fakeBrowser) *browser.Conn {
	t.Helper()
	conn, err := browser.Open(context.Background(), fake.server.Listener.Addr().String(), fake.server.Client())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestDiscoverFindsTheBrowserEndpoint(t *testing.T) {
	fake := newFakeBrowser(t)
	url, err := browser.Discover(context.Background(), fake.server.Listener.Addr().String(), fake.server.Client())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if !strings.HasPrefix(url, "ws://") || !strings.HasSuffix(url, "/devtools/browser/test") {
		t.Fatalf("discovered %q, want the browser websocket url", url)
	}
}

func TestContextAndTargetLifecycle(t *testing.T) {
	fake := newFakeBrowser(t)
	conn := open(t, fake)
	ctx := context.Background()

	contextID, err := conn.NewContext(ctx)
	if err != nil {
		t.Fatalf("new context: %v", err)
	}
	targetID, err := conn.NewTarget(ctx, "about:blank", contextID)
	if err != nil {
		t.Fatalf("new target: %v", err)
	}
	targets, err := conn.Targets(ctx)
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	if len(targets) != 1 || targets[0] != targetID {
		t.Fatalf("targets = %v, want just %s", targets, targetID)
	}

	if err := conn.CloseTarget(ctx, targetID); err != nil {
		t.Fatalf("close target: %v", err)
	}
	if err := conn.DisposeContext(ctx, contextID); err != nil {
		t.Fatalf("dispose context: %v", err)
	}
	fake.mu.Lock()
	pages := len(fake.targets)
	contexts := len(fake.contexts)
	fake.mu.Unlock()
	if pages != 0 || contexts != 0 {
		t.Fatalf("after cleanup: %d targets, %d contexts, want none", pages, contexts)
	}

	// Disposing something already gone is success, not an error: cleanup runs
	// after a crash.
	if err := conn.DisposeContext(ctx, contextID); err != nil {
		t.Fatalf("disposing a gone context returned %v, want nil", err)
	}
}

func TestBrowserRefusalKeepsItsOwnError(t *testing.T) {
	// This is the exact failure the HTTP shim hid behind "502 Bad Gateway".
	fake := newFakeBrowser(t)
	conn := open(t, fake)
	fake.mu.Lock()
	fake.refuse = true
	fake.mu.Unlock()

	_, err := conn.NewContext(context.Background())
	if err == nil {
		t.Fatalf("a refused command returned no error")
	}
	var cdpErr *browser.CDPError
	if !asCDPError(err, &cdpErr) {
		t.Fatalf("error is %T (%v), want *browser.CDPError", err, err)
	}
	if cdpErr.Code != -32000 || cdpErr.Message != "Not allowed" {
		t.Fatalf("error = %+v, want code -32000 message \"Not allowed\"", cdpErr)
	}
}

func asCDPError(err error, target **browser.CDPError) bool {
	for err != nil {
		if typed, ok := err.(*browser.CDPError); ok {
			*target = typed
			return true
		}
		type unwrapper interface{ Unwrap() error }
		unwrapped, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}

func TestSessionCommandsCarryTheirSession(t *testing.T) {
	fake := newFakeBrowser(t)
	conn := open(t, fake)
	ctx := context.Background()

	contextID, _ := conn.NewContext(ctx)
	targetID, _ := conn.NewTarget(ctx, "about:blank", contextID)
	session, err := conn.Attach(ctx, targetID)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if session.ID != "sess-"+string(targetID) {
		t.Fatalf("session id = %q, want sess-%s", session.ID, targetID)
	}

	if err := session.EnableMetrics(ctx); err != nil {
		t.Fatalf("enable metrics: %v", err)
	}
	usage, err := session.Metrics(ctx)
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if usage.TaskDurationSeconds != 1.5 || usage.JSHeapUsedBytes != 1048576 {
		t.Fatalf("usage = %+v, want the values the browser reported", usage)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("close session: %v", err)
	}
}

func TestNavigationLoadsAndMetricsAreDeltas(t *testing.T) {
	fake := newFakeBrowser(t)
	conn := open(t, fake)
	ctx := context.Background()

	contextID, _ := conn.NewContext(ctx)
	targetID, _ := conn.NewTarget(ctx, "about:blank", contextID)
	session, err := conn.Attach(ctx, targetID)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := session.EnableMetrics(ctx); err != nil {
		t.Fatalf("enable metrics: %v", err)
	}
	if err := session.EnablePage(ctx); err != nil {
		t.Fatalf("enable page: %v", err)
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
	if delta.TaskDurationSeconds != 0 {
		t.Fatalf("delta = %+v, want zero when the counters did not move", delta)
	}
	if after.JSHeapUsedBytes != before.JSHeapUsedBytes {
		t.Fatalf("heap is a gauge, not a counter: %v -> %v", before.JSHeapUsedBytes, after.JSHeapUsedBytes)
	}
}

func TestSessionsDoNotStealEachOthersEvents(t *testing.T) {
	fake := newFakeBrowser(t)
	conn := open(t, fake)
	ctx := context.Background()

	contextA, _ := conn.NewContext(ctx)
	targetA, _ := conn.NewTarget(ctx, "about:blank", contextA)
	contextB, _ := conn.NewContext(ctx)
	targetB, _ := conn.NewTarget(ctx, "about:blank", contextB)

	sessionA, err := conn.Attach(ctx, targetA)
	if err != nil {
		t.Fatalf("attach A: %v", err)
	}
	sessionB, err := conn.Attach(ctx, targetB)
	if err != nil {
		t.Fatalf("attach B: %v", err)
	}
	if err := sessionB.EnablePage(ctx); err != nil {
		t.Fatalf("enable page B: %v", err)
	}
	if _, err := sessionB.Navigate(ctx, "https://example.com/"); err != nil {
		t.Fatalf("navigate B: %v", err)
	}
	if err := sessionB.WaitForLoad(ctx); err != nil {
		t.Fatalf("session B never saw its own load event: %v", err)
	}
	// A must not have received B's event: its stream is separate.
	select {
	case event := <-sessionA.Events():
		t.Fatalf("session A received %q, which belongs to session B", event.Method)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUnknownCommandIsAnError(t *testing.T) {
	fake := newFakeBrowser(t)
	conn := open(t, fake)
	if _, err := conn.Do(context.Background(), "Nope.missing", nil); err == nil {
		t.Fatalf("an unknown method returned no error")
	}
}

func TestCancelledCommandReturnsInsteadOfHanging(t *testing.T) {
	fake := newFakeBrowser(t)
	conn := open(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := conn.Do(ctx, "Target.getTargets", nil); err == nil {
		t.Fatalf("a cancelled command returned no error")
	}
}

func TestConnectionClosureSurfacesAsErr(t *testing.T) {
	fake := newFakeBrowser(t)
	conn := open(t, fake)
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn.Err() != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connection closed without recording an error")
}

func TestUsageDeltaIgnoresCountersGoingBackwards(t *testing.T) {
	before := browser.Usage{TaskDurationSeconds: 2, ScriptDurationSeconds: 1, LayoutCount: 5}
	after := browser.Usage{TaskDurationSeconds: 1.5, ScriptDurationSeconds: 1, LayoutCount: 5, JSHeapUsedBytes: 900}
	delta := after.Delta(before)
	if delta.TaskDurationSeconds != 0 {
		t.Fatalf("a counter that went backwards produced %v, want 0", delta.TaskDurationSeconds)
	}
	if delta.JSHeapUsedBytes != 900 {
		t.Fatalf("heap = %v, want the gauge value 900", delta.JSHeapUsedBytes)
	}
}
