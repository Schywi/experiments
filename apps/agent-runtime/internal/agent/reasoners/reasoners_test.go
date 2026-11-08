package reasoners

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

func completionServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, string(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testInput() agent.AgentInput {
	snap := agent.Snapshot{
		Version: 13,
		URL:     "http://page.test/",
		Nodes: []agent.Node{
			{Ref: agent.Ref{Version: 13, ID: 1}, Role: "button", Name: "Sign in", Box: agent.Box{X: 1, Y: 1, W: 4, H: 4}},
			{Ref: agent.Ref{Version: 13, ID: 2}, Role: "button", Name: "Delete card", Box: agent.Box{X: 5, Y: 5, W: 4, H: 4}},
		},
	}
	return agent.AgentInput{
		Objective:  "click Sign in",
		Step:       1,
		Snapshot:   snap,
		Candidates: snap.Nodes,
		Budget:     agent.Budget{MaxOps: 5},
	}
}

func TestQwenParsesAFencedDecision(t *testing.T) {
	content := "```json\n" + `{"kind":"plan","ops":[{"action":"click","ref":"13:1"}],"why":"sign in"}` + "\n```"
	srv := completionServer(t, content)
	q := NewQwen(srv.URL, "qwen3-vl-2b-instruct")

	d, err := q.Decide(context.Background(), testInput())
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if d.Kind != agent.Plan || len(d.Ops) != 1 {
		t.Fatalf("decision = %+v", d)
	}
	if d.Ops[0].Action != agent.ActionClick || d.Ops[0].Ref != (agent.Ref{Version: 13, ID: 1}) {
		t.Fatalf("op = %+v", d.Ops[0])
	}
	if d.Source != "qwen" {
		t.Fatalf("source = %q", d.Source)
	}
}

func TestQwenReadsDoneAndFail(t *testing.T) {
	for content, want := range map[string]agent.DecisionKind{
		`{"kind":"done","why":"met"}`: agent.Done,
		`{"kind":"fail","why":"gone"}`: agent.Fail,
	} {
		srv := completionServer(t, content)
		d, err := NewQwen(srv.URL, "m").Decide(context.Background(), testInput())
		if err != nil {
			t.Fatalf("%s: %v", content, err)
		}
		if d.Kind != want {
			t.Fatalf("%s: kind = %s, want %s", content, d.Kind, want)
		}
	}
}

func TestQwenRejectsProseAndUnknownKinds(t *testing.T) {
	for _, content := range []string{
		"I think the button is near the top left.",
		`{"kind":"maybe","why":"unsure"}`,
		`{"kind":"plan","ops":[{"action":"click","ref":"nonsense"}]}`,
	} {
		srv := completionServer(t, content)
		if _, err := NewQwen(srv.URL, "m").Decide(context.Background(), testInput()); err == nil {
			t.Fatalf("%q was accepted as a decision", content)
		}
	}
}

func TestQwenNeedsAnEndpoint(t *testing.T) {
	if _, err := NewQwen("", "m").Decide(context.Background(), testInput()); err == nil {
		t.Fatal("an unconfigured planner must fail loudly")
	}
}

func TestPromptListsEveryRef(t *testing.T) {
	text := UserPrompt(testInput())
	for _, want := range []string{"Snapshot 13", "13:1", "13:2", "Sign in", "click Sign in"} {
		if !strings.Contains(text, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, text)
		}
	}
}

func systemOneServer(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLayaPicksACandidateByRef(t *testing.T) {
	srv := systemOneServer(t, `{"target":"13:2"}`)
	d, err := NewLaya(srv.URL).Decide(context.Background(), testInput())
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if d.Kind != agent.Plan || len(d.Ops) != 1 {
		t.Fatalf("decision = %+v", d)
	}
	if d.Ops[0].Ref != (agent.Ref{Version: 13, ID: 2}) {
		t.Fatalf("ref = %s, want 13:2", d.Ops[0].Ref)
	}
	if d.Source != "laya" {
		t.Fatalf("source = %q", d.Source)
	}
}

func TestLayaPicksACandidateByName(t *testing.T) {
	srv := systemOneServer(t, `{"result":{"choice":"Sign in"}}`)
	d, err := NewLaya(srv.URL).Decide(context.Background(), testInput())
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if d.Ops[0].Ref != (agent.Ref{Version: 13, ID: 1}) {
		t.Fatalf("ref = %s, want 13:1", d.Ops[0].Ref)
	}
}

func TestLayaRefusesAnEmptyCandidateSet(t *testing.T) {
	srv := systemOneServer(t, `{"target":"13:1"}`)
	in := testInput()
	in.Candidates = nil
	if _, err := NewLaya(srv.URL).Decide(context.Background(), in); err != ErrNoCandidates {
		t.Fatalf("err = %v, want ErrNoCandidates", err)
	}
}

func TestLayaReportsARepliesThatNamesNothing(t *testing.T) {
	srv := systemOneServer(t, `{"target":"the checkout page"}`)
	if _, err := NewLaya(srv.URL).Decide(context.Background(), testInput()); err == nil {
		t.Fatal("a label outside the candidate set must be an error")
	}
}

type stubReasoner struct {
	name string
	d    agent.Decision
	err  error
	used bool
}

func (s *stubReasoner) Name() string { return s.name }

func (s *stubReasoner) Decide(_ context.Context, _ agent.AgentInput) (agent.Decision, error) {
	s.used = true
	return s.d, s.err
}

func TestPickerAnswersWithoutThePlanner(t *testing.T) {
	picker := &stubReasoner{name: "picker", d: agent.Decision{Kind: agent.Plan,
		Ops: []agent.Op{{Action: agent.ActionClick, Ref: agent.Ref{Version: 13, ID: 1}}}}}
	planner := &stubReasoner{name: "planner"}
	combo := NewPickerThenPlanner(picker, planner)

	d, err := combo.Decide(context.Background(), testInput())
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if planner.used {
		t.Fatal("the planner was called although the picker answered")
	}
	if d.Source != "picker" {
		t.Fatalf("source = %q, want the picker", d.Source)
	}
}

func TestPickerFallsBackWhenTheCandidatesCannotAnswer(t *testing.T) {
	picker := &stubReasoner{name: "picker", err: ErrNoCandidates}
	planner := &stubReasoner{name: "planner", d: agent.Decision{Kind: agent.Done, Why: "met"}}
	combo := NewPickerThenPlanner(picker, planner)

	d, err := combo.Decide(context.Background(), testInput())
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if !planner.used {
		t.Fatal("the planner was not consulted")
	}
	if d.Source != "planner" {
		t.Fatalf("source = %q, want the planner", d.Source)
	}
}

func TestPickerPropagatesATransportFailure(t *testing.T) {
	picker := &stubReasoner{name: "picker", err: fmt.Errorf("HTTP 500")}
	planner := &stubReasoner{name: "planner", d: agent.Decision{Kind: agent.Done}}
	combo := NewPickerThenPlanner(picker, planner)
	if _, err := combo.Decide(context.Background(), testInput()); err == nil {
		t.Fatal("a transport failure must not be swallowed as a fallback")
	}
}
