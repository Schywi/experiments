package reasoners

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/agent-runtime/internal/agent"
)

// Qwen is a free-form planner over an OpenAI-compatible chat completions endpoint,
// which is what llama-server exposes for the GGUF model in the cluster.
type Qwen struct {
	Endpoint    string
	Model       string
	MaxTokens   int
	Temperature float64
	HTTP        *http.Client
}

// NewQwen returns a planner. An empty endpoint leaves the reasoner unusable, which
// the server reports rather than silently swapping in another model.
func NewQwen(endpoint, model string) *Qwen {
	return &Qwen{
		Endpoint:  endpoint,
		Model:     model,
		MaxTokens: 320,
		HTTP:      &http.Client{Timeout: 120 * time.Second},
	}
}

// Name implements agent.Named.
func (q *Qwen) Name() string { return "qwen" }

type wireOp struct {
	Action string `json:"action"`
	Ref    string `json:"ref"`
	Text   string `json:"text"`
	URL    string `json:"url"`
	DY     int    `json:"dy"`
}

type wireDecision struct {
	Kind string   `json:"kind"`
	Ops  []wireOp `json:"ops"`
	Why  string   `json:"why"`
}

// Decide implements agent.Reasoner.
func (q *Qwen) Decide(ctx context.Context, in agent.AgentInput) (agent.Decision, error) {
	if q.Endpoint == "" {
		return agent.Decision{}, fmt.Errorf("qwen: no endpoint configured")
	}
	payload := map[string]any{
		"model":       q.Model,
		"temperature": q.Temperature,
		"max_tokens":  q.maxTokens(),
		"messages": []map[string]string{
			{"role": "system", "content": SystemPrompt},
			{"role": "user", "content": UserPrompt(in)},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return agent.Decision{}, fmt.Errorf("qwen: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.Endpoint, bytes.NewReader(encoded))
	if err != nil {
		return agent.Decision{}, fmt.Errorf("qwen: request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	client := q.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return agent.Decision{}, fmt.Errorf("qwen: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return agent.Decision{}, fmt.Errorf("qwen: read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return agent.Decision{}, fmt.Errorf("qwen: HTTP %d: %s", resp.StatusCode, truncate(string(body)))
	}
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &completion); err != nil {
		return agent.Decision{}, fmt.Errorf("qwen: decode completion: %w", err)
	}
	if len(completion.Choices) == 0 {
		return agent.Decision{}, fmt.Errorf("qwen: no choices")
	}
	return parseDecision(completion.Choices[0].Message.Content)
}

func (q *Qwen) maxTokens() int {
	if q.MaxTokens > 0 {
		return q.MaxTokens
	}
	return 320
}

// parseDecision reads one JSON object out of a reply that may be wrapped in prose
// or a code fence. Anything else is an error the runner records as a failed run.
func parseDecision(content string) (agent.Decision, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return agent.Decision{}, fmt.Errorf("qwen: reply carries no JSON object: %s", truncate(content))
	}
	var wire wireDecision
	if err := json.Unmarshal([]byte(content[start:end+1]), &wire); err != nil {
		return agent.Decision{}, fmt.Errorf("qwen: reply is not the decision schema: %w", err)
	}
	out := agent.Decision{Why: wire.Why, Source: "qwen"}
	switch strings.ToLower(strings.TrimSpace(wire.Kind)) {
	case "done":
		out.Kind = agent.Done
	case "fail":
		out.Kind = agent.Fail
	case "plan":
		out.Kind = agent.Plan
	default:
		return agent.Decision{}, fmt.Errorf("qwen: unknown kind %q", wire.Kind)
	}
	for _, w := range wire.Ops {
		op := agent.Op{Action: strings.ToLower(strings.TrimSpace(w.Action)), Text: w.Text, URL: w.URL, DY: w.DY}
		if w.Ref != "" {
			ref, err := agent.ParseRef(w.Ref)
			if err != nil {
				return agent.Decision{}, fmt.Errorf("qwen: %w", err)
			}
			op.Ref = ref
		}
		out.Ops = append(out.Ops, op)
	}
	return out, nil
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
