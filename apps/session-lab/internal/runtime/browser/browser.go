package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Discover reads Chromium's browser WebSocket URL from its own /json/version,
// reached at addr (host:port). The URL Chromium reports already uses the address
// it sees, which is the pod IP the caller dialled.
func Discover(ctx context.Context, addr string, client *http.Client) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if !strings.Contains(addr, "/") {
		addr = "http://" + addr
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(addr, "/")+"/json/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("discover %s: %w", addr, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("discover %s: %s", addr, resp.Status)
	}
	var info struct {
		URL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("discover %s: decode: %w", addr, err)
	}
	if info.URL == "" {
		return "", fmt.Errorf("discover %s: no webSocketDebuggerUrl", addr)
	}
	return info.URL, nil
}

// Open discovers and dials a browser in one step.
func Open(ctx context.Context, addr string, client *http.Client) (*Conn, error) {
	url, err := Discover(ctx, addr, client)
	if err != nil {
		return nil, err
	}
	return Dial(ctx, url)
}

// ContextID identifies one browser context: an isolated cookie, storage and cache
// namespace inside a shared Chromium.
type ContextID string

// TargetID identifies one page.
type TargetID string

// NewContext creates an isolated browsing context.
func (c *Conn) NewContext(ctx context.Context) (ContextID, error) {
	result, err := c.Do(ctx, "Target.createBrowserContext", map[string]any{
		// Keep the context alive when no client is attached; the runtime owns its
		// lifetime and disposes it explicitly.
		"disposeOnDetach": false,
	})
	if err != nil {
		return "", err
	}
	var decoded struct {
		ID string `json:"browserContextId"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return "", fmt.Errorf("createBrowserContext: decode: %w", err)
	}
	if decoded.ID == "" {
		return "", fmt.Errorf("createBrowserContext: empty id")
	}
	return ContextID(decoded.ID), nil
}

// DisposeContext destroys a context and everything in it.
func (c *Conn) DisposeContext(ctx context.Context, id ContextID) error {
	_, err := c.Do(ctx, "Target.disposeBrowserContext", map[string]any{"browserContextId": string(id)})
	if err != nil && isGone(err) {
		return nil
	}
	return err
}

// NewTarget opens a page inside a context.
func (c *Conn) NewTarget(ctx context.Context, url string, id ContextID) (TargetID, error) {
	if url == "" {
		url = "about:blank"
	}
	result, err := c.Do(ctx, "Target.createTarget", map[string]any{
		"url": url, "browserContextId": string(id),
	})
	if err != nil {
		return "", err
	}
	var decoded struct {
		ID string `json:"targetId"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return "", fmt.Errorf("createTarget: decode: %w", err)
	}
	if decoded.ID == "" {
		return "", fmt.Errorf("createTarget: empty id")
	}
	return TargetID(decoded.ID), nil
}

// CloseTarget closes one page.
func (c *Conn) CloseTarget(ctx context.Context, id TargetID) error {
	_, err := c.Do(ctx, "Target.closeTarget", map[string]any{"targetId": string(id)})
	if err != nil && isGone(err) {
		return nil
	}
	return err
}

// Targets lists the pages the browser knows about, for verification.
func (c *Conn) Targets(ctx context.Context) ([]TargetID, error) {
	result, err := c.Do(ctx, "Target.getTargets", nil)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Infos []struct {
			ID   string `json:"targetId"`
			Type string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return nil, err
	}
	out := make([]TargetID, 0, len(decoded.Infos))
	for _, info := range decoded.Infos {
		out = append(out, TargetID(info.ID))
	}
	return out, nil
}

// isGone reports whether an error means the object was already removed. Cleanup
// paths treat that as success.
func isGone(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no target with given id") ||
		strings.Contains(message, "failed to find context") ||
		strings.Contains(message, "no session with given id") ||
		strings.Contains(message, "no such target")
}
