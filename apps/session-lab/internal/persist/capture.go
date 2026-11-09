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
	// The page itself, so a restore comes back to the same URL rather than the
	// site root. Best-effort: a page without a location restores to its origin.
	if href, err := evalString(ctx, session, "location.href"); err == nil {
		blob.URL = href
	}
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
	// IndexedDB and Cache Storage are read through the page: one has a read-only
	// CDP domain, the other has none. A page that cannot answer contributes
	// nothing, and its error is carried in the blob rather than dropped: a
	// silently empty surface is how a run reports a success it did not have.
	state, err := ReadPageState(ctx, session)
	if err != nil {
		return Blob{}, fmt.Errorf("snapshot: %w", err)
	}
	blob.IndexedDB = state.IndexedDB
	blob.Cache = state.Cache
	blob.IndexedDBError = state.IndexedDBError
	blob.CacheError = state.CacheError
	blob.CacheProbe = state.CacheProbe
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
	if len(blob.Local) == 0 && len(blob.IndexedDB) == 0 && len(blob.Cache) == 0 {
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
		// The page the session was on, not its bare origin: the state belongs to
		// the URL's origin either way, and the restored session should land where
		// the work was.
		target := blob.URL
		if target == "" {
			target = origin
		}
		if _, err := session.Navigate(ctx, target); err != nil {
			return fmt.Errorf("restore: navigate %s: %w", target, err)
		}
		if err := session.WaitForLoad(ctx); err != nil {
			return fmt.Errorf("restore: load %s: %w", target, err)
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

	// IndexedDB and Cache Storage need a document at the origin. A blob that holds
	// only those still has to land on its page first.
	if len(origins) == 0 && (len(blob.IndexedDB) > 0 || len(blob.Cache) > 0) {
		target := blob.URL
		if target == "" {
			target = blob.Origin
		}
		if !usableOrigin(target) {
			return fmt.Errorf("restore: no page to put the state on")
		}
		if _, err := session.Navigate(ctx, target); err != nil {
			return fmt.Errorf("restore: navigate %s: %w", target, err)
		}
		if err := session.WaitForLoad(ctx); err != nil {
			return fmt.Errorf("restore: load %s: %w", target, err)
		}
	}

	if len(blob.IndexedDB) > 0 || len(blob.Cache) > 0 {
		state := PageState{IndexedDB: blob.IndexedDB, Cache: blob.Cache}
		if err := WritePageState(ctx, session, state); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
		// The page has to run again with that state present: a document that
		// loaded before the write cannot see it.
		if _, err := session.Do(ctx, "Page.reload", nil); err != nil {
			return fmt.Errorf("restore: reload: %w", err)
		}
		if err := session.WaitForLoad(ctx); err != nil {
			return fmt.Errorf("restore: load after reload: %w", err)
		}
	}
	return nil
}

// Verified is what a restored context holds, measured against the blob.
type Verified struct {
	Cookies       int `json:"cookies_restored"`
	CookiesWanted int `json:"cookies_stored"`
	Items         int `json:"items_restored"`
	ItemsWanted   int `json:"items_stored"`
	Records       int `json:"idb_records_restored"`
	RecordsWanted int `json:"idb_records_stored"`
	Entries       int `json:"cache_entries_restored"`
	EntriesWanted int `json:"cache_entries_stored"`
}

// Complete reports whether every surface the blob held came back. A surface the
// blob never carried is not missing anything.
func (v Verified) Complete() bool {
	return v.Cookies == v.CookiesWanted &&
		v.Items == v.ItemsWanted &&
		v.Records == v.RecordsWanted &&
		v.Entries == v.EntriesWanted
}

// Verify reports what a context holds after a restore, comparing against the
// blob: cookies, localStorage items, IndexedDB records and Cache Storage entries.
// A restore that returns nil but leaves nothing behind shows up here as zeros.
func Verify(ctx context.Context, conn *browser.Conn, session *browser.Session, id browser.ContextID, blob Blob) (Verified, error) {
	var verified Verified
	got, err := cookiesFor(ctx, conn, id)
	if err != nil {
		return verified, err
	}
	verified.CookiesWanted = len(blob.Cookies)
	wantCookies := map[string]bool{}
	for _, raw := range blob.Cookies {
		var parsed struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			continue
		}
		wantCookies[parsed.Name+"="+parsed.Value] = true
	}
	for _, raw := range got {
		var parsed struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			continue
		}
		if wantCookies[parsed.Name+"="+parsed.Value] {
			verified.Cookies++
		}
	}
	verified.ItemsWanted = blob.Items()
	for origin, list := range blob.Local {
		if !usableOrigin(origin) {
			continue
		}
		read, err := storageItems(ctx, session, origin)
		if err != nil {
			continue
		}
		have := map[string]bool{}
		for _, item := range read {
			have[item.Name+"="+item.Value] = true
		}
		for _, item := range list {
			if have[item.Name+"="+item.Value] {
				verified.Items++
			}
		}
	}
	verified.RecordsWanted = blob.IDBRecords()
	verified.EntriesWanted = blob.CacheEntries()
	if verified.RecordsWanted > 0 || verified.EntriesWanted > 0 {
		records, entries, err := CountPageState(ctx, session)
		if err != nil {
			return verified, err
		}
		verified.Records = min(records, verified.RecordsWanted)
		verified.Entries = min(entries, verified.EntriesWanted)
	}
	return verified, nil
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
