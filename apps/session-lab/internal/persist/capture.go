package persist

// The CDP walk. Every call here goes through a connection the runtime already
// has: cookies are browser-scoped with the context named as a parameter, and
// localStorage is read through the flattened session attached to the context's
// page.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
)

// Snapshot reads the context's restorable state. The session must be attached to
// a page in that context: the origin comes from that page's document, and
// localStorage is read per origin.
func Snapshot(ctx context.Context, conn *browser.Conn, session *browser.Session, id browser.ContextID) (Blob, error) {
	if conn == nil {
		return Blob{}, fmt.Errorf("snapshot: no connection")
	}
	if session == nil {
		return Blob{}, fmt.Errorf("snapshot: no session")
	}

	cookies, err := cookiesFor(ctx, conn, id)
	if err != nil {
		return Blob{}, err
	}
	blob := Blob{Version: Version, Cookies: cookies}

	origin, err := evalString(ctx, session, "location.origin")
	if err != nil {
		return Blob{}, fmt.Errorf("snapshot: origin: %w", err)
	}
	blob.Origin = origin
	if !usableOrigin(origin) {
		// about:blank and a data: page have no origin storage, so there is
		// nothing to read and nothing to restore.
		return blob, nil
	}
	if _, err := session.Do(ctx, "DOMStorage.enable", nil); err != nil {
		return Blob{}, fmt.Errorf("snapshot: DOMStorage.enable: %w", err)
	}
	items, err := storageItems(ctx, session, origin)
	if err != nil {
		return Blob{}, err
	}
	if len(items) > 0 {
		blob.Local = map[string][]Item{origin: items}
	}
	return blob, nil
}

// Restore writes a blob into a context. Cookies go first and through the browser
// connection, so a navigation after this carries them. localStorage needs a
// document at its origin, so each origin is navigated to before its items are
// written; the context is left on the last origin restored.
func Restore(ctx context.Context, conn *browser.Conn, session *browser.Session, id browser.ContextID, blob Blob) error {
	if conn == nil {
		return fmt.Errorf("restore: no connection")
	}
	if blob.Version != Version {
		return fmt.Errorf("restore: blob version %d, this build reads %d", blob.Version, Version)
	}
	if len(blob.Cookies) > 0 {
		if _, err := conn.Do(ctx, "Storage.setCookies", map[string]any{
			"cookies":          blob.Cookies,
			"browserContextId": string(id),
		}); err != nil {
			return fmt.Errorf("restore: setCookies: %w", err)
		}
	}
	if len(blob.Local) == 0 {
		return nil
	}
	if session == nil {
		return fmt.Errorf("restore: localStorage needs a session")
	}

	// Sorted so a restore visits origins in the same order every time.
	origins := make([]string, 0, len(blob.Local))
	for origin := range blob.Local {
		origins = append(origins, origin)
	}
	sort.Strings(origins)

	if _, err := session.Do(ctx, "DOMStorage.enable", nil); err != nil {
		return fmt.Errorf("restore: DOMStorage.enable: %w", err)
	}
	for _, origin := range origins {
		if !usableOrigin(origin) {
			continue
		}
		if _, err := session.Navigate(ctx, origin); err != nil {
			return fmt.Errorf("restore: navigate %s: %w", origin, err)
		}
		if err := session.WaitForLoad(ctx); err != nil {
			return fmt.Errorf("restore: load %s: %w", origin, err)
		}
		for _, item := range blob.Local[origin] {
			if _, err := session.Do(ctx, "DOMStorage.setDOMStorageItem", map[string]any{
				"storageId": map[string]any{"securityOrigin": origin, "isLocalStorage": true},
				"key":       item.Name,
				"value":     item.Value,
			}); err != nil {
				return fmt.Errorf("restore: set %s/%s: %w", origin, item.Name, err)
			}
		}
	}
	return nil
}

// cookiesFor reads the context's cookies, HttpOnly included, replayable as they
// came.
func cookiesFor(ctx context.Context, conn *browser.Conn, id browser.ContextID) ([]json.RawMessage, error) {
	result, err := conn.Do(ctx, "Storage.getCookies", map[string]any{
		"browserContextId": string(id),
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot: getCookies: %w", err)
	}
	var decoded struct {
		Cookies []json.RawMessage `json:"cookies"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return nil, fmt.Errorf("snapshot: getCookies decode: %w", err)
	}
	return decoded.Cookies, nil
}

// storageItems reads every localStorage entry of one origin.
func storageItems(ctx context.Context, session *browser.Session, origin string) ([]Item, error) {
	result, err := session.Do(ctx, "DOMStorage.getDOMStorageItems", map[string]any{
		"storageId": map[string]any{"securityOrigin": origin, "isLocalStorage": true},
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot: getDOMStorageItems %s: %w", origin, err)
	}
	var decoded struct {
		Entries [][2]string `json:"entries"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return nil, fmt.Errorf("snapshot: getDOMStorageItems decode: %w", err)
	}
	items := make([]Item, 0, len(decoded.Entries))
	for _, entry := range decoded.Entries {
		items = append(items, Item{Name: entry[0], Value: entry[1]})
	}
	return items, nil
}

// evalString evaluates an expression that returns a string.
func evalString(ctx context.Context, session *browser.Session, expression string) (string, error) {
	result, err := session.Do(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
	})
	if err != nil {
		return "", err
	}
	var decoded struct {
		Result struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return "", fmt.Errorf("evaluate %q: decode: %w", expression, err)
	}
	if decoded.ExceptionDetails != nil {
		return "", fmt.Errorf("evaluate %q: %s", expression, decoded.ExceptionDetails.Text)
	}
	return decoded.Result.Value, nil
}

// usableOrigin reports whether an origin has storage behind it. about:blank
// reports "null"; a data: URL has an opaque origin.
func usableOrigin(origin string) bool {
	switch origin {
	case "", "null", "about:blank":
		return false
	}
	return true
}
