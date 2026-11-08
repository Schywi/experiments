package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeReasoner struct {
	name      string
	decisions []Decision
	err       error
	calls     int
}

func (f *fakeReasoner) Name() string {
	if f.name == "" {
		return "fake"
	}
	return f.name
}

func (f *fakeReasoner) Decide(_ context.Context, _ AgentInput) (Decision, error) {
	f.calls++
	if f.err != nil {
		return Decision{}, f.err
	}
	if len(f.decisions) == 0 {
		return Decision{Kind: Fail, Why: "no decisions left"}, nil
	}
	d := f.decisions[0]
	f.decisions = f.decisions[1:]
	return d, nil
}

type fakeInspect struct {
	snapshot Snapshot
	err      error
	calls    int
}

func (f *fakeInspect) Name() string { return ToolInspect }

func (f *fakeInspect) Execute(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	f.calls++
	if f.err != nil {
		return ToolResult{}, f.err
	}
	snap := f.snapshot
	return ToolResult{Snapshot: &snap}, nil
}

type fakeAct struct {
	calls int
	got   []Op
}

func (f *fakeAct) Name() string { return ToolAct }

func (f *fakeAct) Execute(_ context.Context, args json.RawMessage) (ToolResult, error) {
	f.calls++
	var in struct {
		Ops []Op `json:"ops"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ToolResult{}, err
	}
	f.got = append(f.got, in.Ops...)
	out := ToolResult{}
	for _, op := range in.Ops {
		out.Ops = append(out.Ops, ActionResult{Op: op, OK: true})
	}
	return out, nil
}

func clickOp(id int) Op { return Op{Action: ActionClick, Ref: Ref{Version: 13, ID: id}} }

func testRunner(reasoner Reasoner) (*Runner, *fakeInspect, *fakeAct) {
	inspect := &fakeInspect{snapshot: testSnapshot()}
	act := &fakeAct{}
	return NewRunner(reasoner, map[string]Tool{ToolInspect: inspect, ToolAct: act},
		testPolicy(), Budget{MaxInferences: 8, MaxOps: 20}), inspect, act
}

func TestRunnerDispatchesAGatedBatchThenFinishes(t *testing.T) {
	reasoner := &fakeReasoner{decisions: []Decision{
		{Kind: Plan, Ops: []Op{clickOp(1), clickOp(1)}, Why: "both"},
		{Kind: Done, Why: "clicked"},
	}}
	runner, inspect, act := testRunner(reasoner)

	res, err := runner.Run(context.Background(), Task{RunID: "run-1", Objective: "click sign in"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.State != StateDone {
		t.Fatalf("state = %s, detail = %q", res.State, res.Detail)
	}
	if res.Ops != 2 || res.Inferences != 2 || res.Steps != 2 {
		t.Fatalf("result = %+v, want 2 ops, 2 inferences, 2 steps", res)
	}
	if act.calls != 1 || len(act.got) != 2 {
		t.Fatalf("act calls = %d, ops = %d, want one batch of two", act.calls, len(act.got))
	}
	if inspect.calls != 2 {
		t.Fatalf("inspect calls = %d, want 2 (one per step)", inspect.calls)
	}
}

func TestRunnerRejectsEveryOpWithoutDispatching(t *testing.T) {
	reasoner := &fakeReasoner{decisions: []Decision{{
		Kind: Plan,
		Ops:  []Op{{Action: ActionClick, Ref: Ref{Version: 12, ID: 1}}},
	}}}
	runner, _, act := testRunner(reasoner)

	res, err := runner.Run(context.Background(), Task{RunID: "run-2", Objective: "click"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.State != StateFailed || !strings.Contains(res.Detail, RejectStaleRef) {
		t.Fatalf("result = %+v, want failed on %s", res, RejectStaleRef)
	}
	if res.Rejected != 1 {
		t.Fatalf("rejected = %d, want 1", res.Rejected)
	}
	if act.calls != 0 {
		t.Fatalf("act calls = %d, want 0: a rejected op must not reach the browser", act.calls)
	}
}

func TestRunnerStopsOnTheInferenceBudget(t *testing.T) {
	reasoner := &fakeReasoner{decisions: []Decision{
		{Kind: Plan, Ops: []Op{clickOp(1)}},
		{Kind: Plan, Ops: []Op{clickOp(1)}},
	}}
	runner, _, _ := testRunner(reasoner)
	runner.Budget.MaxInferences = 1

	res, err := runner.Run(context.Background(), Task{RunID: "run-3", Objective: "click"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.State != StateBudgetExhausted || res.Detail != "inference budget" {
		t.Fatalf("result = %+v, want budget-exhausted on the inference budget", res)
	}
	if res.Inferences != 1 || reasoner.calls != 1 {
		t.Fatalf("inferences = %d, calls = %d, want 1 and 1", res.Inferences, reasoner.calls)
	}
}

func TestRunnerFailsOnAnEmptyPlan(t *testing.T) {
	runner, _, act := testRunner(&fakeReasoner{decisions: []Decision{{Kind: Plan, Why: "nothing"}}})
	res, err := runner.Run(context.Background(), Task{RunID: "run-4"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.State != StateFailed || res.Detail != "plan with no ops" {
		t.Fatalf("result = %+v", res)
	}
	if act.calls != 0 {
		t.Fatalf("act calls = %d, want 0", act.calls)
	}
}

func TestRunnerAttributesInferencesToTheDecidingReasoner(t *testing.T) {
	reasoner := &fakeReasoner{decisions: []Decision{
		{Kind: Plan, Ops: []Op{clickOp(1)}, Source: "laya"},
		{Kind: Done, Source: "qwen"},
	}}
	runner, _, _ := testRunner(reasoner)
	res, err := runner.Run(context.Background(), Task{RunID: "run-5"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.InferencesByReasoner["laya"] != 1 || res.InferencesByReasoner["qwen"] != 1 {
		t.Fatalf("by reasoner = %+v, want one each", res.InferencesByReasoner)
	}
}

func TestRunnerResolvesWithoutAnInference(t *testing.T) {
	runner, _, _ := testRunner(&fakeReasoner{})
	runner.Resolve = func(in AgentInput) (Decision, bool) {
		for _, a := range in.LastActions {
			if a.OK && a.Op.Action == ActionClick {
				return Decision{Kind: Done, Source: "dom", Why: "clicked"}, true
			}
		}
		return Decision{Kind: Plan, Source: "dom", Ops: []Op{clickOp(1)}}, true
	}
	res, err := runner.Run(context.Background(), Task{RunID: "run-6"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.State != StateDone || res.Inferences != 0 {
		t.Fatalf("result = %+v, want done with zero inferences", res)
	}
	if res.Ops != 1 {
		t.Fatalf("ops = %d, want 1", res.Ops)
	}
}

func TestRunnerReportsAnInspectFailure(t *testing.T) {
	inspect := &fakeInspect{err: errors.New("console unreachable")}
	runner := NewRunner(&fakeReasoner{}, map[string]Tool{ToolInspect: inspect, ToolAct: &fakeAct{}},
		testPolicy(), Budget{})
	res, err := runner.Run(context.Background(), Task{RunID: "run-7"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.State != StateFailed || !strings.Contains(res.Detail, "inspect") {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunnerReportsAReasonerFailure(t *testing.T) {
	runner, _, _ := testRunner(&fakeReasoner{err: errors.New("qwen: HTTP 503")})
	res, err := runner.Run(context.Background(), Task{RunID: "run-8"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.State != StateFailed || !strings.Contains(res.Detail, "503") {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunnerCancelsBeforeTheFirstStep(t *testing.T) {
	runner, _, _ := testRunner(&fakeReasoner{decisions: []Decision{{Kind: Done}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := runner.Run(ctx, Task{RunID: "run-9"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.State != StateCancelled {
		t.Fatalf("result = %+v, want cancelled", res)
	}
}

func TestRunnerNeedsBothTools(t *testing.T) {
	runner := NewRunner(&fakeReasoner{}, map[string]Tool{ToolInspect: &fakeInspect{}}, testPolicy(), Budget{})
	if _, err := runner.Run(context.Background(), Task{}); err == nil {
		t.Fatal("a runner without an act tool must return an error, not a scored result")
	}
}

func TestRunnerEmitsLifecycleEvents(t *testing.T) {
	reasoner := &fakeReasoner{decisions: []Decision{
		{Kind: Plan, Ops: []Op{clickOp(1)}},
		{Kind: Done},
	}}
	runner, _, _ := testRunner(reasoner)
	var kinds []string
	runner.Emit = func(e Event) { kinds = append(kinds, e.Kind) }
	if _, err := runner.Run(context.Background(), Task{RunID: "run-10"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{
		EventRunStarted,
		EventPageInspected, EventDecided, EventActionCompleted,
		EventPageInspected, EventDecided,
		EventRunCompleted,
	}
	if len(kinds) != len(want) {
		t.Fatalf("events = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event %d = %s, want %s", i, kinds[i], want[i])
		}
	}
}
