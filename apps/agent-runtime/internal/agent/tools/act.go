package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lucasmirandoliveira/experiments/apps/agent-runtime/internal/agent"
)

// ActTool executes operations that have already passed the policy check.
//
// It re-checks the two things only the browser can answer: that the ref still
// belongs to the document the snapshot described, and that the document has not
// been replaced mid-batch. A batch stops at the first of either, so a navigation
// triggered by op 1 never lets op 2 land on a page the reasoner did not see.
type ActTool struct {
	Client    *Client
	SandboxID string
	Page      *Page
}

// NewAct returns an act tool for one sandbox.
func NewAct(client *Client, sandboxID string, page *Page) *ActTool {
	return &ActTool{Client: client, SandboxID: sandboxID, Page: page}
}

// Name implements agent.Tool.
func (t *ActTool) Name() string { return agent.ToolAct }

// Execute implements agent.Tool.
func (t *ActTool) Execute(ctx context.Context, args json.RawMessage) (agent.ToolResult, error) {
	var in struct {
		Ops []agent.Op `json:"ops"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return agent.ToolResult{}, fmt.Errorf("tools: act args: %w", err)
		}
	}
	base, have := t.Page.Get()
	if !have {
		return agent.ToolResult{}, fmt.Errorf("tools: act before inspect")
	}

	result := agent.ToolResult{}
	for i, op := range in.Ops {
		if code, ok := t.dispatch(ctx, base, op); !ok {
			result.Ops = append(result.Ops, agent.ActionResult{Op: op, OK: false, Detail: code})
			result.Detail = code
			return result, nil
		}
		result.Ops = append(result.Ops, agent.ActionResult{Op: op, OK: true})

		if i == len(in.Ops)-1 {
			break
		}
		// More ops follow: prove the document is still the one they were planned
		// against before dispatching the next one.
		if changed, detail := t.documentChanged(ctx, base); changed {
			t.Page.Invalidate()
			result.Detail = detail
			return result, nil
		}
	}
	return result, nil
}

func (t *ActTool) dispatch(ctx context.Context, base agent.Snapshot, op agent.Op) (code string, ok bool) {
	switch op.Action {
	case agent.ActionClick:
		node, found := base.Node(op.Ref)
		if !found {
			return "unknown-ref", false
		}
		x, y := node.Box.Center()
		if err := t.Client.Click(ctx, t.SandboxID, x, y); err != nil {
			return "click-failed: " + err.Error(), false
		}
		return "", true
	case agent.ActionType:
		node, found := base.Node(op.Ref)
		if !found {
			return "unknown-ref", false
		}
		x, y := node.Box.Center()
		if err := t.Client.Click(ctx, t.SandboxID, x, y); err != nil {
			return "focus-failed: " + err.Error(), false
		}
		if _, err := t.Client.CDP(ctx, t.SandboxID, "Input.insertText",
			map[string]any{"text": op.Text}); err != nil {
			return "type-failed: " + err.Error(), false
		}
		return "", true
	case agent.ActionScroll:
		if err := t.Client.Scroll(ctx, t.SandboxID, op.DY); err != nil {
			return "scroll-failed: " + err.Error(), false
		}
		return "", true
	case agent.ActionNavigate:
		if op.URL == "" {
			return "bad-op", false
		}
		if err := t.Client.Navigate(ctx, t.SandboxID, op.URL); err != nil {
			return "navigate-failed: " + err.Error(), false
		}
		t.Page.Invalidate()
		return "", true
	}
	return "bad-op", false
}

func (t *ActTool) documentChanged(ctx context.Context, base agent.Snapshot) (bool, string) {
	raw, err := t.Client.Evaluate(ctx, t.SandboxID,
		`(() => JSON.stringify({url: location.href, origin: performance.timeOrigin}))()`)
	if err != nil {
		return false, ""
	}
	var inner string
	if err := json.Unmarshal(raw, &inner); err != nil {
		return false, ""
	}
	var out struct {
		URL    string  `json:"url"`
		Origin float64 `json:"origin"`
	}
	if err := json.Unmarshal([]byte(inner), &out); err != nil {
		return false, ""
	}
	current := fmt.Sprintf("%s|%d", out.URL, int64(out.Origin))
	if base.Fingerprint != "" && current != base.Fingerprint {
		return true, "page-changed: batch stopped"
	}
	return false, ""
}
