package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Tool names the runner asks for.
const (
	ToolInspect = "inspect_page"
	ToolAct     = "act"
)

// Runner is the default AgentRunner.
//
// It owns the budget and the order of operations; the reasoner owns only the
// choice of what to do next. That split is what makes the two dials the E7 plan
// varies — cadence and reasoner — into configuration rather than code.
type Runner struct {
	Reasoner Reasoner
	Tools    map[string]Tool
	Policy   Policy
	Budget   Budget
	MaxSteps int
	Now      func() time.Time
	Emit     func(Event)

	// resolve, when set, answers a step without calling the reasoner. It is how a
	// run reaches the DOM-only path E6 measured: when the accessibility tree names
	// exactly one node, no model is needed to pick it.
	Resolve func(AgentInput) (Decision, bool)
}

// NewRunner returns a runner with defaults filled in.
func NewRunner(reasoner Reasoner, tools map[string]Tool, policy Policy, budget Budget) *Runner {
	return &Runner{
		Reasoner: reasoner,
		Tools:    tools,
		Policy:   policy,
		Budget:   budget,
		MaxSteps: 8,
		Now:      func() time.Time { return time.Now().UTC() },
	}
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

func (r *Runner) emit(e Event) {
	if r.Emit != nil {
		r.Emit(e)
	}
}

// Run executes one task. A model or transport failure ends the run as failed with
// the error in Detail; only a context cancellation or a missing tool returns an
// error, because those are defects in the caller rather than outcomes to score.
func (r *Runner) Run(ctx context.Context, task Task) (Result, error) {
	res := Result{State: StateRunning, InferencesByReasoner: map[string]int{}}
	inspect, ok := r.Tools[ToolInspect]
	if !ok {
		return res, fmt.Errorf("agent: runner has no %q tool", ToolInspect)
	}
	act, ok := r.Tools[ToolAct]
	if !ok {
		return res, fmt.Errorf("agent: runner has no %q tool", ToolAct)
	}
	maxSteps := r.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 8
	}
	r.emit(Event{Kind: EventRunStarted, RunID: task.RunID, Detail: task.Objective})

	var history []ActionResult
	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			res.State = StateCancelled
			res.Detail = "context cancelled"
			res.Steps = step
			r.complete(task, &res)
			return res, nil
		}
		if r.Budget.Expired(r.now()) {
			res.State = StateBudgetExhausted
			res.Detail = "deadline"
			res.Steps = step
			r.complete(task, &res)
			return res, nil
		}

		t0 := r.now()
		out, err := inspect.Execute(ctx, json.RawMessage(`{}`))
		if err != nil {
			res.State = StateFailed
			res.Detail = "inspect: " + err.Error()
			res.Steps = step
			r.complete(task, &res)
			return res, nil
		}
		if out.Snapshot == nil {
			res.State = StateFailed
			res.Detail = "inspect: no snapshot"
			res.Steps = step
			r.complete(task, &res)
			return res, nil
		}
		snap := *out.Snapshot
		r.emit(Event{Kind: EventPageInspected, RunID: task.RunID, Step: step,
			SnapshotVersion: snap.Version, Nodes: len(snap.Nodes),
			DurationMS: millis(r.now().Sub(t0))})

		input := AgentInput{
			Objective:   task.Objective,
			Step:        step,
			Snapshot:    snap,
			Candidates:  snap.Nodes,
			LastActions: history,
			Budget:      r.Budget,
		}

		var decision Decision
		resolved := false
		if r.Resolve != nil {
			if d, ok := r.Resolve(input); ok {
				decision, resolved = d, true
			}
		}
		if !resolved {
			if r.Budget.MaxInferences > 0 && res.Inferences >= r.Budget.MaxInferences {
				res.State = StateBudgetExhausted
				res.Detail = "inference budget"
				res.Steps = step
				r.complete(task, &res)
				return res, nil
			}
			if r.Reasoner == nil {
				res.State = StateFailed
				res.Detail = "no reasoner"
				res.Steps = step
				r.complete(task, &res)
				return res, nil
			}
			t1 := r.now()
			d, err := r.Reasoner.Decide(ctx, input)
			name := d.Source
			if name == "" {
				name = ReasonerName(r.Reasoner)
			}
			res.Inferences++
			res.InferencesByReasoner[name]++
			if err != nil {
				res.State = StateFailed
				res.Detail = name + ": " + err.Error()
				res.Steps = step
				r.complete(task, &res)
				return res, nil
			}
			decision = d
			r.emit(Event{Kind: EventDecided, RunID: task.RunID, Step: step,
				SnapshotVersion: snap.Version, Inferences: res.Inferences, Ops: len(d.Ops),
				DurationMS: millis(r.now().Sub(t1)), Detail: name + ":" + string(d.Kind)})
		}
		res.Steps = step

		switch decision.Kind {
		case Done:
			res.State = StateDone
			res.Detail = decision.Why
			r.complete(task, &res)
			return res, nil
		case Fail:
			res.State = StateFailed
			res.Detail = decision.Why
			r.complete(task, &res)
			return res, nil
		case Plan:
		default:
			res.State = StateFailed
			res.Detail = "unknown decision kind " + string(decision.Kind)
			r.complete(task, &res)
			return res, nil
		}
		if len(decision.Ops) == 0 {
			res.State = StateFailed
			res.Detail = "plan with no ops"
			r.complete(task, &res)
			return res, nil
		}

		allowedOps, rejects := Check(r.Policy, snap, decision.Ops)
		for _, rj := range rejects {
			res.Rejected++
			r.emit(Event{Kind: EventActionRejected, RunID: task.RunID, Step: step,
				SnapshotVersion: snap.Version, Detail: rj.Code + ": " + rj.Detail})
		}
		if len(allowedOps) == 0 {
			res.State = StateFailed
			res.Detail = "every op rejected (" + rejects[0].Code + ")"
			r.complete(task, &res)
			return res, nil
		}
		if r.Budget.MaxOps > 0 {
			left := r.Budget.MaxOps - res.Ops
			if left <= 0 {
				res.State = StateBudgetExhausted
				res.Detail = "op budget"
				r.complete(task, &res)
				return res, nil
			}
			if len(allowedOps) > left {
				allowedOps = allowedOps[:left]
			}
		}

		payload, err := json.Marshal(struct {
			Ops []Op `json:"ops"`
		}{allowedOps})
		if err != nil {
			return res, fmt.Errorf("agent: encode ops: %w", err)
		}
		t2 := r.now()
		acted, err := act.Execute(ctx, payload)
		if err != nil {
			res.State = StateFailed
			res.Detail = "act: " + err.Error()
			r.complete(task, &res)
			return res, nil
		}
		dispatched := 0
		for _, ar := range acted.Ops {
			if ar.OK {
				dispatched++
			}
		}
		res.Ops += dispatched
		history = append(history, acted.Ops...)
		r.emit(Event{Kind: EventActionCompleted, RunID: task.RunID, Step: step,
			SnapshotVersion: snap.Version, Ops: dispatched,
			DurationMS: millis(r.now().Sub(t2)), Detail: acted.Detail})
	}

	res.State = StateBudgetExhausted
	res.Detail = "step limit"
	r.complete(task, &res)
	return res, nil
}

func (r *Runner) complete(task Task, res *Result) {
	r.emit(Event{Kind: EventRunCompleted, RunID: task.RunID, Step: res.Steps,
		Inferences: res.Inferences, Ops: res.Ops, Detail: string(res.State)})
}

func millis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

// ExactlyOne returns the single visible node whose name matches the needle, and
// reports whether it was unique. It is the resolver a run uses when the tree
// already answers the question, which is the case E6 measured at 7 of 8 tasks.
func ExactlyOne(snap Snapshot, needle string) (Node, bool) {
	matches := snap.Named(needle)
	if len(matches) != 1 {
		return Node{}, false
	}
	return matches[0], true
}
