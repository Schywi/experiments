package reasoners

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/agent-runtime/internal/agent"
)

var (
	// ErrNoCandidates means the step offered nothing to choose from.
	ErrNoCandidates = errors.New("laya: no candidates")
	// ErrNoMatch means the reply named nothing in the candidate set.
	ErrNoMatch = errors.New("laya: reply named no candidate")
)

// Laya is a closed-set picker. `/v1/systemone` answers a choice question over the
// criteria it is handed, so it can select one of the snapshot's refs and cannot
// author an operation of its own. That is the entire reason it is here: it costs
// a few hundred milliseconds of CPU instead of a GPU inference.
type Laya struct {
	Endpoint      string
	MaxCandidates int
	HTTP          *http.Client
}

// NewLaya returns a picker pointed at a `/v1/systemone` endpoint.
func NewLaya(endpoint string) *Laya {
	return &Laya{Endpoint: endpoint, MaxCandidates: 40, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// Name implements agent.Named.
func (l *Laya) Name() string { return "laya" }

// Decide implements agent.Reasoner. It returns ErrNoCandidates when the snapshot is
// empty, which the composite reasoner treats as "ask the planner instead".
func (l *Laya) Decide(ctx context.Context, in agent.AgentInput) (agent.Decision, error) {
	if l.Endpoint == "" {
		return agent.Decision{}, fmt.Errorf("laya: no endpoint configured")
	}
	candidates := in.Candidates
	if len(candidates) == 0 {
		return agent.Decision{}, ErrNoCandidates
	}
	if max := l.MaxCandidates; max > 0 && len(candidates) > max {
		candidates = candidates[:max]
	}
	instructions, criteria, _ := candidatePrompt(agent.AgentInput{
		Objective: in.Objective, Candidates: candidates,
	})
	payload := map[string]any{
		"state": map[string]any{
			"body":      in.Objective,
			"url":       in.Snapshot.URL,
			"snapshot":  in.Snapshot.Version,
			"candidate": len(candidates),
		},
		"questions": map[string]any{
			"target": map[string]any{
				"type":         "choice",
				"instructions": instructions,
				"criteria":     criteria,
			},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return agent.Decision{}, fmt.Errorf("laya: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.Endpoint, bytes.NewReader(encoded))
	if err != nil {
		return agent.Decision{}, fmt.Errorf("laya: request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	client := l.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return agent.Decision{}, fmt.Errorf("laya: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return agent.Decision{}, fmt.Errorf("laya: read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return agent.Decision{}, fmt.Errorf("laya: HTTP %d: %s", resp.StatusCode, truncate(string(body)))
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return agent.Decision{}, fmt.Errorf("laya: decode reply: %w", err)
	}
	label := firstLabel(decoded, "target")
	if label == "" {
		return agent.Decision{}, ErrNoMatch
	}
	ref, ok := matchCandidate(label, candidates)
	if !ok {
		return agent.Decision{}, fmt.Errorf("%w: %q", ErrNoMatch, label)
	}
	return agent.Decision{
		Kind:   agent.Plan,
		Ops:    []agent.Op{{Action: agent.ActionClick, Ref: ref}},
		Why:    "laya picked " + ref.String() + " (" + label + ")",
		Source: "laya",
	}, nil
}

// firstLabel digs one string out of a reply whose envelope varies between builds.
// The keys are the ones apps/assistant/laya_planner.py reads, plus the question
// name, so a change on the server side shows up in one place.
func firstLabel(value any, question string) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		for _, key := range []string{question, "choice", "label", "result", "decision", "output", "answer"} {
			if inner, ok := v[key]; ok {
				if s := firstLabel(inner, question); s != "" {
					return s
				}
			}
		}
		for _, inner := range v {
			if s := firstLabel(inner, question); s != "" {
				return s
			}
		}
	case []any:
		for _, inner := range v {
			if s := firstLabel(inner, question); s != "" {
				return s
			}
		}
	}
	return ""
}

func matchCandidate(label string, candidates []agent.Node) (agent.Ref, bool) {
	needle := strings.ToLower(strings.TrimSpace(label))
	if needle == "" {
		return agent.Ref{}, false
	}
	for _, n := range candidates {
		if strings.EqualFold(n.Ref.String(), needle) {
			return n.Ref, true
		}
	}
	for _, n := range candidates {
		if strings.EqualFold(n.Name, needle) {
			return n.Ref, true
		}
	}
	for _, n := range candidates {
		if strings.Contains(strings.ToLower(n.Name), needle) {
			return n.Ref, true
		}
	}
	return agent.Ref{}, false
}
