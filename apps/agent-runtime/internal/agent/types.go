// Package agent owns one unit of browser work: an objective, a reasoner that
// decides what to do next, tools that do it, and the budget the run may spend.
//
// The package knows nothing about users, quotas, sessions or Chromium. Its only
// contact with the outside world is the Reasoner and Tool interfaces below, which
// cmd/ satisfies with HTTP clients. Package user is deliberately absent from this
// package's imports, and a test in cmd/ enforces that.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DecisionKind is what the reasoner concluded about the current step.
type DecisionKind string

const (
	// Plan means the operations in the decision should be dispatched.
	Plan DecisionKind = "plan"
	// Done means the objective is met and the run ends successfully.
	Done DecisionKind = "done"
	// Fail means the objective cannot be met from the current page.
	Fail DecisionKind = "fail"
)

// Ref names one node inside one snapshot version. A ref from an older version is
// stale by construction: version 13 and version 14 may both have a node 5, and
// they are different elements.
type Ref struct {
	Version int `json:"version"`
	ID      int `json:"id"`
}

// String renders the ref as "version:id", the form a reasoner emits.
func (r Ref) String() string {
	return strconv.Itoa(r.Version) + ":" + strconv.Itoa(r.ID)
}

// IsZero reports whether the ref names nothing.
func (r Ref) IsZero() bool { return r.Version == 0 && r.ID == 0 }

// ParseRef reads "version:id".
func ParseRef(s string) (Ref, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return Ref{}, fmt.Errorf("agent: ref %q is not version:id", s)
	}
	version, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return Ref{}, fmt.Errorf("agent: ref %q has no integer version", s)
	}
	id, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return Ref{}, fmt.Errorf("agent: ref %q has no integer id", s)
	}
	return Ref{Version: version, ID: id}, nil
}

// Box is an element's layout box in viewport pixels.
type Box struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Center is the point a click is dispatched at.
func (b Box) Center() (x, y float64) {
	return float64(b.X) + float64(b.W)/2, float64(b.Y) + float64(b.H)/2
}

// Empty reports whether the box has no area, which makes the node unactionable.
func (b Box) Empty() bool { return b.W <= 0 || b.H <= 0 }

// Node is one element as the reasoner sees it. It carries no `backendNodeId` on
// purpose: the tool layer keeps that mapping, so a reasoner can only name what
// the snapshot showed it.
type Node struct {
	Ref      Ref
	Role     string
	Name     string
	Box      Box
	Disabled bool
}

// Label is the compact form used in prompts: `button "Sign in"`.
func (n Node) Label() string {
	if n.Name == "" {
		return n.Role
	}
	return n.Role + " " + strconv.Quote(n.Name)
}

// Snapshot is one view of the page. Its version is the key that makes refs safe.
type Snapshot struct {
	Version     int    `json:"version"`
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint"`
	Nodes       []Node `json:"nodes"`
}

// Node looks a ref up inside this snapshot.
func (s Snapshot) Node(ref Ref) (Node, bool) {
	for _, n := range s.Nodes {
		if n.Ref == ref {
			return n, true
		}
	}
	return Node{}, false
}

// Named returns nodes whose accessible name contains the needle, case-insensitively.
func (s Snapshot) Named(needle string) []Node {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return nil
	}
	var out []Node
	for _, n := range s.Nodes {
		if strings.Contains(strings.ToLower(n.Name), needle) {
			out = append(out, n)
		}
	}
	return out
}

// The operations a run may dispatch. The set is closed: a tool that does not
// appear here cannot be reached by a reasoner.
const (
	ActionClick    = "click"
	ActionType     = "type"
	ActionScroll   = "scroll"
	ActionNavigate = "navigate"
)

// Actions is the full operation vocabulary, used by configuration defaults.
func Actions() []string {
	return []string{ActionClick, ActionType, ActionScroll, ActionNavigate}
}

// Op is one browser operation. Ref is meaningful for click and type; URL for
// navigate; DY for scroll.
type Op struct {
	Action string `json:"action"`
	Ref    Ref    `json:"ref,omitempty"`
	Text   string `json:"text,omitempty"`
	URL    string `json:"url,omitempty"`
	DY     int    `json:"dy,omitempty"`
}

// ActionResult is what happened to one op.
type ActionResult struct {
	Op     Op     `json:"op"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Budget bounds one run. The runner holds it; a reasoner only reads it.
type Budget struct {
	MaxInferences int
	MaxOps        int
	Deadline      time.Time
}

// Expired reports whether the deadline has passed.
func (b Budget) Expired(now time.Time) bool {
	return !b.Deadline.IsZero() && now.After(b.Deadline)
}

// AgentInput is everything a reasoner is given for one step.
//
// Candidates is the closed set a picker reasoner chooses from. It is filled with
// the visible nodes on every step, so a reasoner that can only answer a choice
// question over a fixed set has everything it needs.
type AgentInput struct {
	Objective   string
	Step        int
	Snapshot    Snapshot
	Candidates  []Node
	LastActions []ActionResult
	Budget      Budget
}

// Decision is what the reasoner concluded.
//
// Source names the reasoner that produced it, which matters when a composite
// reasoner is in use: a picker that answered a step must not be counted as a
// planner inference.
type Decision struct {
	Kind   DecisionKind
	Ops    []Op
	Why    string
	Source string
}

// ToolResult is what a tool reports back.
type ToolResult struct {
	Snapshot *Snapshot      `json:"snapshot,omitempty"`
	Ops      []ActionResult `json:"ops,omitempty"`
	Detail   string         `json:"detail,omitempty"`
}

// Task is one run's input.
type Task struct {
	RunID     string
	AgentID   string
	Objective string
}

// State is where a run ended.
type State string

const (
	StateRunning         State = "running"
	StateDone            State = "done"
	StateFailed          State = "failed"
	StateCancelled       State = "cancelled"
	StateBudgetExhausted State = "budget-exhausted"
)

// Result is a finished run. Inferences is counted per reasoner so a run that
// never called the model (a picker that failed over to nothing, or a run that
// finished from a deterministic resolve) is visible as zero.
type Result struct {
	State                State          `json:"state"`
	Steps                int            `json:"steps"`
	Ops                  int            `json:"ops"`
	Rejected             int            `json:"rejected"`
	Inferences           int            `json:"inferences"`
	InferencesByReasoner map[string]int `json:"inferences_by_reasoner,omitempty"`
	Detail               string         `json:"detail,omitempty"`
}

// Reasoner decides what to do next. This is the whole model-facing surface.
type Reasoner interface {
	Decide(ctx context.Context, input AgentInput) (Decision, error)
}

// Tool does one thing to the outside world.
type Tool interface {
	Name() string
	Execute(ctx context.Context, args json.RawMessage) (ToolResult, error)
}

// AgentRunner executes one task to a result.
type AgentRunner interface {
	Run(ctx context.Context, task Task) (Result, error)
}

// Named is implemented by reasoners that want to be counted by name.
type Named interface{ Name() string }

// ReasonerName reports a reasoner's name, or a stable placeholder.
func ReasonerName(r Reasoner) string {
	if n, ok := r.(Named); ok {
		if name := n.Name(); name != "" {
			return name
		}
	}
	return "reasoner"
}
