// Package reasoners implements agent.Reasoner for the two models the study uses:
// Qwen as a free-form planner and Laya as a closed-set picker over the candidate
// refs a snapshot offers.
package reasoners

import (
	"strconv"
	"strings"

	"github.com/lucasmirandoliveira/experiments/apps/agent-runtime/internal/agent"
)

// SystemPrompt is the fixed instruction both planning calls share. Warmer wording
// is not varied between arms: the dialect under test is the cadence, and E5 already
// showed that prompt wording moves this class of model more than it should.
const SystemPrompt = `You drive a browser through three tools.

Reply with exactly one JSON object and nothing else:
{"kind":"plan","ops":[{"action":"click","ref":"V:N"}],"why":"..."}
{"kind":"done","why":"..."}
{"kind":"fail","why":"..."}

Actions: click (needs ref), type (needs ref and text), scroll (needs dy),
navigate (needs url). A ref is "VERSION:ID" exactly as listed. Up to five ops may
appear in one plan; they are dispatched in order against the snapshot shown.
Use "done" only when the objective is met on the page as it stands.`

// UserPrompt renders one step's input. The layout is stable across calls so a
// comparison between arms is not shifted by formatting.
func UserPrompt(in agent.AgentInput) string {
	var b strings.Builder
	b.WriteString("Objective: ")
	b.WriteString(in.Objective)
	b.WriteString("\n\nSnapshot ")
	b.WriteString(strconv.Itoa(in.Snapshot.Version))
	b.WriteString(" at ")
	b.WriteString(in.Snapshot.URL)
	b.WriteString("\n")
	for _, n := range in.Snapshot.Nodes {
		b.WriteString("  ")
		b.WriteString(n.Ref.String())
		b.WriteString("  ")
		b.WriteString(n.Label())
		if n.Disabled {
			b.WriteString(" (disabled)")
		}
		b.WriteString("\n")
	}
	if len(in.LastActions) > 0 {
		b.WriteString("\nAlready dispatched:\n")
		for _, a := range in.LastActions {
			b.WriteString("  ")
			b.WriteString(a.Op.Action)
			if !a.Op.Ref.IsZero() {
				b.WriteString(" ")
				b.WriteString(a.Op.Ref.String())
			}
			if a.Op.Text != "" {
				b.WriteString(" ")
				b.WriteString(strconv.Quote(a.Op.Text))
			}
			if a.OK {
				b.WriteString("  ok")
			} else {
				b.WriteString("  not dispatched")
			}
			b.WriteString("\n")
		}
	}
	if in.Budget.MaxOps > 0 {
		b.WriteString("\nOperations remaining: ")
		b.WriteString(strconv.Itoa(in.Budget.MaxOps))
		b.WriteString("\n")
	}
	return b.String()
}

// candidatePrompt is the picker's variant: a numbered choice over the nodes.
func candidatePrompt(in agent.AgentInput) (instructions string, criteria map[string]string, keys []string) {
	criteria = make(map[string]string, len(in.Candidates))
	for _, n := range in.Candidates {
		key := n.Ref.String()
		criteria[key] = n.Label()
		keys = append(keys, key)
	}
	instructions = "Which element satisfies this objective: " + in.Objective +
		"? Choose the ref of one element from the criteria."
	return instructions, criteria, keys
}
