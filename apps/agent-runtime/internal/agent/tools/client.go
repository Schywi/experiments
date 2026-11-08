// Package tools implements the two Tools the runner dispatches: inspect_page,
// which reads the page into a versioned snapshot, and act, which executes gated
// operations against it.
//
// Everything here talks to apps/sandbox-console over its own HTTP API. Nothing
// in this package knows about users, quotas or reasoners.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Defaults for the console the experiment drives. They match the addresses E6's
// controller used, so a run reproduces the earlier setup.
const (
	DefaultConsole = "http://sandbox-console.sandbox-console.svc.cluster.local:8080"
	DefaultRouter  = "http://sandbox-router-svc.agent-sandbox-system.svc.cluster.local:8080"
	DefaultNS      = "sandbox-demo"
	DefaultPort    = "9222"
	tokenFile      = "/var/run/secrets/kubernetes.io/serviceaccount/token"
)

// ConsoleError is a failed console or router call, keeping the status and body.
type ConsoleError struct {
	Method string
	URL    string
	Status int
	Body   string
	Err    error
}

func (e *ConsoleError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s %s: %v", e.Method, e.URL, e.Err)
	}
	if e.Body != "" {
		return fmt.Sprintf("%s %s -> HTTP %d: %s", e.Method, e.URL, e.Status, e.Body)
	}
	return fmt.Sprintf("%s %s -> HTTP %d", e.Method, e.URL, e.Status)
}

// Unwrap exposes the transport error.
func (e *ConsoleError) Unwrap() error { return e.Err }

// Client is the console's HTTP API plus the router's DevTools passthrough.
type Client struct {
	Console   string
	Router    string
	Namespace string
	Port      string
	Token     string
	HTTP      *http.Client
}

// NewClient fills in the cluster defaults for anything left empty.
func NewClient(console, router string) *Client {
	if console == "" {
		console = DefaultConsole
	}
	if router == "" {
		router = DefaultRouter
	}
	return &Client{
		Console:   strings.TrimRight(console, "/"),
		Router:    strings.TrimRight(router, "/"),
		Namespace: DefaultNS,
		Port:      DefaultPort,
		HTTP:      &http.Client{Timeout: 120 * time.Second},
	}
}

// Claim takes one sandbox from the warm pool and returns its id.
func (c *Client) Claim(ctx context.Context, url string) (string, error) {
	payload := map[string]any{"kind": "claim"}
	if url != "" {
		payload["url"] = url
	}
	var body struct {
		Sandbox string `json:"sandbox"`
	}
	if err := c.do(ctx, http.MethodPost, c.Console+"/sandboxes", payload, nil, &body); err != nil {
		return "", err
	}
	if body.Sandbox == "" {
		return "", &ConsoleError{Method: http.MethodPost, URL: c.Console + "/sandboxes",
			Body: "claim did not bind a sandbox"}
	}
	return body.Sandbox, nil
}

// Release deletes a claimed sandbox.
func (c *Client) Release(ctx context.Context, sandboxID string) error {
	return c.do(ctx, http.MethodDelete, c.Console+"/sandboxes/"+sandboxID, nil, nil, nil)
}

// Navigate sends the sandbox to a URL.
func (c *Client) Navigate(ctx context.Context, sandboxID, url string) error {
	return c.do(ctx, http.MethodPost, c.Console+"/api/sandboxes/"+sandboxID+"/url",
		map[string]any{"url": url}, nil, nil)
}

// Click dispatches a real press and release at viewport coordinates.
func (c *Client) Click(ctx context.Context, sandboxID string, x, y float64) error {
	return c.do(ctx, http.MethodPost, c.Console+"/api/sandboxes/"+sandboxID+"/click",
		map[string]any{"x": x, "y": y, "scale": 1.0}, nil, nil)
}

// Scroll dispatches a real wheel event.
func (c *Client) Scroll(ctx context.Context, sandboxID string, dy int) error {
	return c.do(ctx, http.MethodPost, c.Console+"/api/sandboxes/"+sandboxID+"/scroll",
		map[string]any{"dy": dy}, nil, nil)
}

// CDP calls one DevTools method through the console's router.
func (c *Client) CDP(ctx context.Context, sandboxID, method string, params any) (json.RawMessage, error) {
	if params == nil {
		params = map[string]any{}
	}
	headers := map[string]string{
		"X-Sandbox-ID":        sandboxID,
		"X-Sandbox-Port":      c.Port,
		"X-Sandbox-Namespace": c.Namespace,
	}
	if token := c.token(); token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	var body struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	payload := map[string]any{"method": method, "params": params}
	if err := c.do(ctx, http.MethodPost, c.Router+"/cdp", payload, headers, &body); err != nil {
		return nil, err
	}
	if len(body.Error) > 0 && string(body.Error) != "null" {
		return nil, &ConsoleError{Method: "cdp " + method, URL: c.Router + "/cdp",
			Body: string(body.Error)}
	}
	return body.Result, nil
}

// Evaluate runs one expression and returns its value by value.
func (c *Client) Evaluate(ctx context.Context, sandboxID, expression string) (json.RawMessage, error) {
	result, err := c.CDP(ctx, sandboxID, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
	})
	if err != nil {
		return nil, err
	}
	var inner struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &inner); err != nil {
		return nil, fmt.Errorf("tools: evaluate: %w", err)
	}
	return inner.Result.Value, nil
}

func (c *Client) do(ctx context.Context, method, url string, payload any,
	headers map[string]string, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("tools: encode %s %s: %w", method, url, err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return fmt.Errorf("tools: %s %s: %w", method, url, err)
	}
	if payload != nil {
		req.Header.Set("content-type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return &ConsoleError{Method: method, URL: url, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail := string(raw)
		if len(detail) > 400 {
			detail = detail[:400]
		}
		return &ConsoleError{Method: method, URL: url, Status: resp.StatusCode, Body: detail}
	}
	if readErr != nil {
		return fmt.Errorf("tools: read %s %s: %w", method, url, readErr)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("tools: decode %s %s: %w", method, url, err)
	}
	return nil
}

func (c *Client) token() string {
	if c.Token != "" {
		return c.Token
	}
	blob, err := os.ReadFile(tokenFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(blob))
}
