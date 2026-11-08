package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lucasmirandoliveira/experiments/apps/agent-runtime/internal/agent"
)

// fakeConsole answers the console and router routes the tools use. It is a plain
// httptest server so the tools are exercised over the real transport.
type fakeConsole struct {
	origin  float64
	clicks  int
	navigs  int
	typed   []string
	scrolled int
}

func (f *fakeConsole) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/click"):
			f.clicks++
			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(path, "/scroll"):
			f.scrolled++
			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(path, "/url"):
			f.navigs++
			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(path, "/cdp"):
			var body struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch body.Method {
			case "Runtime.evaluate":
				inner := fmt.Sprintf(`{"url":"http://page.test/","origin":%v}`, f.origin)
				encoded, _ := json.Marshal(inner)
				fmt.Fprintf(w, `{"result":{"result":{"value":%s}}}`, encoded)
			case "Accessibility.getFullAXTree":
				fmt.Fprint(w, `{"result":{"nodes":[
					{"role":{"value":"button"},"name":{"value":"Sign in"},"backendDOMNodeId":101},
					{"role":{"value":"button"},"name":{"value":"Delete card"},"backendDOMNodeId":102},
					{"role":{"value":"generic"},"name":{"value":""},"ignored":true}
				]}}`)
			case "DOM.getBoxModel":
				fmt.Fprint(w, `{"result":{"model":{"border":[10,10,50,10,50,30,10,30]}}}`)
			case "Input.insertText":
				var params struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(body.Params, &params)
				f.typed = append(f.typed, params.Text)
				fmt.Fprint(w, `{"result":{}}`)
			default:
				fmt.Fprint(w, `{"result":{}}`)
			}
		default:
			fmt.Fprint(w, `{}`)
		}
	})
}

func newFake(t *testing.T) (*fakeConsole, *Client) {
	t.Helper()
	f := &fakeConsole{origin: 1000}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return f, NewClient(srv.URL, srv.URL)
}

func TestInspectStampsAVersionAndResolvesBoxes(t *testing.T) {
	_, client := newFake(t)
	page := NewPage()
	tool := NewInspect(client, "sb-1", page)

	out, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if out.Snapshot == nil {
		t.Fatal("no snapshot returned")
	}
	snap := *out.Snapshot
	if snap.Version != 1 {
		t.Fatalf("version = %d, want 1", snap.Version)
	}
	if len(snap.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (the ignored node is dropped)", len(snap.Nodes))
	}
	first := snap.Nodes[0]
	if first.Ref != (agent.Ref{Version: 1, ID: 1}) {
		t.Fatalf("first ref = %s, want 1:1", first.Ref)
	}
	if first.Name != "Sign in" || first.Role != "button" {
		t.Fatalf("first node = %+v", first)
	}
	if first.Box.W != 40 || first.Box.H != 20 || first.Box.X != 10 || first.Box.Y != 10 {
		t.Fatalf("box = %+v, want 10,10 40x20", first.Box)
	}
	if backend, ok := page.Backend(first.Ref); !ok || backend != 101 {
		t.Fatalf("backend = %d ok=%v, want 101", backend, ok)
	}

	again, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("second inspect: %v", err)
	}
	if again.Snapshot.Version != 2 {
		t.Fatalf("version = %d, want 2", again.Snapshot.Version)
	}
	if _, ok := page.Backend(first.Ref); ok {
		t.Fatal("a ref from version 1 must not resolve after version 2")
	}
}

func TestActDispatchesAClickAtTheBoxCentre(t *testing.T) {
	f, client := newFake(t)
	page := NewPage()
	snap := page.Stamp(agent.Snapshot{
		URL:         "http://page.test/",
		Fingerprint: "http://page.test/|1000",
		Nodes: []agent.Node{
			{Ref: agent.Ref{ID: 1}, Role: "button", Name: "Sign in", Box: agent.Box{X: 10, Y: 10, W: 40, H: 20}},
		},
	})
	act := NewAct(client, "sb-1", page)
	ref := snap.Nodes[0].Ref

	out, err := act.Execute(context.Background(), mustJSON(t, map[string]any{
		"ops": []agent.Op{{Action: agent.ActionClick, Ref: ref}},
	}))
	if err != nil {
		t.Fatalf("act: %v", err)
	}
	if len(out.Ops) != 1 || !out.Ops[0].OK {
		t.Fatalf("ops = %+v, want one ok", out.Ops)
	}
	if f.clicks != 1 {
		t.Fatalf("clicks = %d, want 1", f.clicks)
	}
}

func TestActStopsTheBatchWhenTheDocumentChanges(t *testing.T) {
	f, client := newFake(t)
	page := NewPage()
	snap := page.Stamp(agent.Snapshot{
		URL:         "http://page.test/",
		Fingerprint: "http://page.test/|1000",
		Nodes: []agent.Node{
			{Ref: agent.Ref{ID: 1}, Role: "button", Name: "Sign in", Box: agent.Box{X: 10, Y: 10, W: 40, H: 20}},
		},
	})
	ref := snap.Nodes[0].Ref
	// The page is replaced between the two ops, which is what T7 in the E7 plan
	// sets up: the second op must never be dispatched.
	f.origin = 2000

	act := NewAct(client, "sb-1", page)
	out, err := act.Execute(context.Background(), mustJSON(t, map[string]any{
		"ops": []agent.Op{
			{Action: agent.ActionClick, Ref: ref},
			{Action: agent.ActionClick, Ref: ref},
		},
	}))
	if err != nil {
		t.Fatalf("act: %v", err)
	}
	if f.clicks != 1 {
		t.Fatalf("clicks = %d, want 1: the batch must stop after the document changed", f.clicks)
	}
	if len(out.Ops) != 1 {
		t.Fatalf("results = %d, want 1", len(out.Ops))
	}
	if !strings.Contains(out.Detail, "page-changed") {
		t.Fatalf("detail = %q, want it to record the stop", out.Detail)
	}
}

func TestActRefusesAnUnknownRef(t *testing.T) {
	f, client := newFake(t)
	page := NewPage()
	snap := page.Stamp(agent.Snapshot{
		Fingerprint: "http://page.test/|1000",
		Nodes: []agent.Node{
			{Ref: agent.Ref{ID: 1}, Role: "button", Name: "Sign in", Box: agent.Box{X: 1, Y: 1, W: 4, H: 4}},
		},
	})
	act := NewAct(client, "sb-1", page)

	out, err := act.Execute(context.Background(), mustJSON(t, map[string]any{
		"ops": []agent.Op{{Action: agent.ActionClick, Ref: agent.Ref{Version: snap.Version, ID: 9}}},
	}))
	if err != nil {
		t.Fatalf("act: %v", err)
	}
	if len(out.Ops) != 1 || out.Ops[0].OK {
		t.Fatalf("ops = %+v, want one failure", out.Ops)
	}
	if out.Ops[0].Detail != "unknown-ref" {
		t.Fatalf("detail = %q", out.Ops[0].Detail)
	}
	if f.clicks != 0 {
		t.Fatalf("clicks = %d, want 0", f.clicks)
	}
}

func TestActTypesIntoTheNamedField(t *testing.T) {
	f, client := newFake(t)
	page := NewPage()
	snap := page.Stamp(agent.Snapshot{
		Fingerprint: "http://page.test/|1000",
		Nodes: []agent.Node{
			{Ref: agent.Ref{ID: 1}, Role: "textbox", Name: "Email", Box: agent.Box{X: 10, Y: 40, W: 100, H: 20}},
		},
	})
	act := NewAct(client, "sb-1", page)

	out, err := act.Execute(context.Background(), mustJSON(t, map[string]any{
		"ops": []agent.Op{{Action: agent.ActionType, Ref: snap.Nodes[0].Ref, Text: "a@b.test"}},
	}))
	if err != nil {
		t.Fatalf("act: %v", err)
	}
	if len(out.Ops) != 1 || !out.Ops[0].OK {
		t.Fatalf("ops = %+v", out.Ops)
	}
	if f.clicks != 1 {
		t.Fatalf("clicks = %d, want 1 (focusing the field)", f.clicks)
	}
	if len(f.typed) != 1 || f.typed[0] != "a@b.test" {
		t.Fatalf("typed = %v", f.typed)
	}
}

func TestActInvalidatesAfterNavigate(t *testing.T) {
	_, client := newFake(t)
	page := NewPage()
	snap := page.Stamp(agent.Snapshot{
		Fingerprint: "http://page.test/|1000",
		Nodes: []agent.Node{
			{Ref: agent.Ref{ID: 1}, Role: "button", Name: "Next", Box: agent.Box{X: 1, Y: 1, W: 4, H: 4}},
		},
	})
	act := NewAct(client, "sb-1", page)

	_, err := act.Execute(context.Background(), mustJSON(t, map[string]any{
		"ops": []agent.Op{{Action: agent.ActionNavigate, URL: "http://page.test/next"}},
	}))
	if err != nil {
		t.Fatalf("act: %v", err)
	}
	if page.Version() != snap.Version+1 {
		t.Fatalf("version = %d, want %d", page.Version(), snap.Version+1)
	}
	if _, ok := page.Backend(snap.Nodes[0].Ref); ok {
		t.Fatal("refs from before a navigation must not resolve")
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return encoded
}
