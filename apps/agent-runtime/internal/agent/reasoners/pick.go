package reasoners

import (
	"context"
	"errors"

	"github.com/lucasmirandoliveira/experiments/apps/agent-runtime/internal/agent"
)

// PickerThenPlanner is the A3 arm: a cheap closed-set picker answers first, and
// the planner is consulted only when the picker cannot.
//
// The decision it returns is attributed to whichever reasoner produced it, so a run
// that never reached the planner reports zero planner inferences even though both
// are wired into the same call.
type PickerThenPlanner struct {
	Picker  agent.Reasoner
	Planner agent.Reasoner
}

// NewPickerThenPlanner pairs the two reasoners.
func NewPickerThenPlanner(picker, planner agent.Reasoner) *PickerThenPlanner {
	return &PickerThenPlanner{Picker: picker, Planner: planner}
}

// Name implements agent.Named.
func (p *PickerThenPlanner) Name() string { return "laya+qwen" }

// Decide implements agent.Reasoner.
func (p *PickerThenPlanner) Decide(ctx context.Context, in agent.AgentInput) (agent.Decision, error) {
	if p.Picker != nil {
		d, err := p.Picker.Decide(ctx, in)
		switch {
		case err == nil && len(d.Ops) > 0:
			if d.Source == "" {
				d.Source = agent.ReasonerName(p.Picker)
			}
			return d, nil
		case err == nil:
			// A picker that returned no operation has not answered the step.
		case errors.Is(err, ErrNoCandidates), errors.Is(err, ErrNoMatch):
			// The closed set did not contain the answer; the planner may.
		default:
			return agent.Decision{}, err
		}
	}
	if p.Planner == nil {
		return agent.Decision{}, errors.New("reasoners: no planner configured")
	}
	d, err := p.Planner.Decide(ctx, in)
	if err != nil {
		return agent.Decision{}, err
	}
	if d.Source == "" {
		d.Source = agent.ReasonerName(p.Planner)
	}
	return d, nil
}
