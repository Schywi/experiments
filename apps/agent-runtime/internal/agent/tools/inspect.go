package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lucasmirandoliveira/experiments/apps/agent-runtime/internal/agent"
)

// InspectTool reads the page into a versioned snapshot: the accessibility tree,
// each node's layout box, and a fingerprint that says which document the snapshot
// describes.
//
// The tree is the default representation because that is what E6 measured working:
// 7 of 8 tasks resolved to exactly one node and clicked the intended one with the
// model never called. Pixels are a separate arm.
type InspectTool struct {
	Client    *Client
	SandboxID string
	Page      *Page
	MaxNodes  int
}

// NewInspect returns an inspect tool with a node cap that keeps a prompt small.
func NewInspect(client *Client, sandboxID string, page *Page) *InspectTool {
	return &InspectTool{Client: client, SandboxID: sandboxID, Page: page, MaxNodes: 40}
}

// Name implements agent.Tool.
func (t *InspectTool) Name() string { return agent.ToolInspect }

// Execute implements agent.Tool.
func (t *InspectTool) Execute(ctx context.Context, _ json.RawMessage) (agent.ToolResult, error) {
	fingerprint, url, err := t.fingerprint(ctx)
	if err != nil {
		return agent.ToolResult{}, err
	}
	inspected, err := t.nodes(ctx)
	if err != nil {
		return agent.ToolResult{}, err
	}
	nodes := make([]agent.Node, 0, len(inspected))
	for _, n := range inspected {
		nodes = append(nodes, n.node)
	}
	snap := t.Page.Stamp(agent.Snapshot{URL: url, Fingerprint: fingerprint, Nodes: nodes})
	for i, n := range snap.Nodes {
		t.Page.RememberBackend(n.Ref, inspected[i].backend)
	}
	return agent.ToolResult{
		Snapshot: &snap,
		Detail:   fmt.Sprintf("%d nodes", len(snap.Nodes)),
	}, nil
}

// inspectedNode is a node plus the DevTools handle that must not leave this package.
type inspectedNode struct {
	node    agent.Node
	backend int
}

func (t *InspectTool) fingerprint(ctx context.Context) (fingerprint, url string, err error) {
	raw, err := t.Client.Evaluate(ctx, t.SandboxID,
		`(() => JSON.stringify({url: location.href, origin: performance.timeOrigin}))()`)
	if err != nil {
		return "", "", err
	}
	var inner string
	if err := json.Unmarshal(raw, &inner); err != nil {
		return "", "", fmt.Errorf("tools: fingerprint: %w", err)
	}
	var out struct {
		URL    string  `json:"url"`
		Origin float64 `json:"origin"`
	}
	if err := json.Unmarshal([]byte(inner), &out); err != nil {
		return "", "", fmt.Errorf("tools: fingerprint body: %w", err)
	}
	return fmt.Sprintf("%s|%d", out.URL, int64(out.Origin)), out.URL, nil
}

func (t *InspectTool) nodes(ctx context.Context) ([]inspectedNode, error) {
	if _, err := t.Client.CDP(ctx, t.SandboxID, "Accessibility.enable", nil); err != nil {
		return nil, err
	}
	raw, err := t.Client.CDP(ctx, t.SandboxID, "Accessibility.getFullAXTree", nil)
	if err != nil {
		return nil, err
	}
	var tree struct {
		Nodes []struct {
			Role struct {
				Value string `json:"value"`
			} `json:"role"`
			Name struct {
				Value string `json:"value"`
			} `json:"name"`
			Ignored         bool `json:"ignored"`
			BackendDOMNodeID int `json:"backendDOMNodeId"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("tools: ax tree: %w", err)
	}

	out := make([]inspectedNode, 0, 16)
	for _, n := range tree.Nodes {
		if n.Ignored || n.BackendDOMNodeID == 0 {
			continue
		}
		role := strings.TrimSpace(n.Role.Value)
		name := strings.TrimSpace(n.Name.Value)
		if role == "" && name == "" {
			continue
		}
		box, ok := t.box(ctx, n.BackendDOMNodeID)
		if !ok || box.Empty() {
			continue
		}
		out = append(out, inspectedNode{
			node: agent.Node{
				Ref:  agent.Ref{ID: len(out) + 1},
				Role: role,
				Name: collapse(name),
				Box:  box,
			},
			backend: n.BackendDOMNodeID,
		})
		if len(out) >= t.maxNodes() {
			break
		}
	}
	return out, nil
}

func (t *InspectTool) box(ctx context.Context, backendNodeID int) (agent.Box, bool) {
	if _, err := t.Client.CDP(ctx, t.SandboxID, "DOM.enable", nil); err != nil {
		return agent.Box{}, false
	}
	raw, err := t.Client.CDP(ctx, t.SandboxID, "DOM.getBoxModel",
		map[string]any{"backendNodeId": backendNodeID})
	if err != nil {
		return agent.Box{}, false
	}
	var model struct {
		Model struct {
			Border []float64 `json:"border"`
		} `json:"model"`
	}
	if err := json.Unmarshal(raw, &model); err != nil {
		return agent.Box{}, false
	}
	quad := model.Model.Border
	if len(quad) < 8 {
		return agent.Box{}, false
	}
	minX, maxX := quad[0], quad[0]
	minY, maxY := quad[1], quad[1]
	for i := 2; i+1 < len(quad); i += 2 {
		if quad[i] < minX {
			minX = quad[i]
		}
		if quad[i] > maxX {
			maxX = quad[i]
		}
		if quad[i+1] < minY {
			minY = quad[i+1]
		}
		if quad[i+1] > maxY {
			maxY = quad[i+1]
		}
	}
	return agent.Box{
		X: int(minX + 0.5),
		Y: int(minY + 0.5),
		W: int(maxX-minX + 0.5),
		H: int(maxY-minY + 0.5),
	}, true
}

func (t *InspectTool) maxNodes() int {
	if t.MaxNodes > 0 {
		return t.MaxNodes
	}
	return 40
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
