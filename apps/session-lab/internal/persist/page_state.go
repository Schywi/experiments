package persist

// IndexedDB and Cache Storage, read and written through the page's own
// JavaScript.
//
// IndexedDB does have a CDP domain, and it only reads — Storage.indexedDB.
// requestDatabaseNames, requestDatabase and a paginated requestData. Cache
// Storage has no CDP domain at all. Reading and writing both through
// Runtime.evaluate on the session's page is one mechanism instead of two, and it
// is available exactly when it is needed: capture happens while the page that
// holds the state is still alive.
//
// The cost is that a page whose origin has no document cannot be read this way,
// which is why a blob with no page never carries these two surfaces.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
)

// readPageStateJS returns every IndexedDB record and every Cache Storage entry as
// one JSON document. Records stay as the JSON text the page held.
const readPageStateJS = `(async function(){
  var out = {indexeddb: [], cache_storage: []};
  try {
    if (typeof indexedDB !== 'undefined' && indexedDB.databases) {
      var infos = await indexedDB.databases();
      for (var i = 0; i < infos.length; i++) {
        var info = infos[i];
        var db = await new Promise(function(resolve, reject){
          var request = indexedDB.open(info.name);
          request.onsuccess = function(){ resolve(request.result); };
          request.onerror = function(){ reject(request.error); };
        });
        var entry = {name: db.name, version: db.version, stores: []};
        var names = Array.prototype.slice.call(db.objectStoreNames);
        for (var s = 0; s < names.length; s++) {
          var store = db.transaction(names[s], 'readonly').objectStore(names[s]);
          var records = await new Promise(function(resolve, reject){
            var request = store.getAll();
            request.onsuccess = function(){ resolve(request.result); };
            request.onerror = function(){ reject(request.error); };
          });
          entry.stores.push({
            name: names[s],
            key_path: store.keyPath ? String(store.keyPath) : '',
            records: records.map(function(value){ return JSON.stringify(value); })
          });
        }
        db.close();
        out.indexeddb.push(entry);
      }
    }
  } catch (e) { out.indexeddb_error = String(e); }
  try {
    out.cache_probe = 'caches=' + (typeof caches);
    if (typeof caches !== 'undefined') {
      var cacheNames = await caches.keys();
      out.cache_probe += ' names=' + JSON.stringify(cacheNames);
      for (var c = 0; c < cacheNames.length; c++) {
        var cache = await caches.open(cacheNames[c]);
        var requests = await cache.keys();
        var cacheEntry = {name: cacheNames[c], entries: []};
        for (var r = 0; r < requests.length; r++) {
          var response = await cache.match(requests[r]);
          if (!response) { continue; }
          var buffer = await response.arrayBuffer();
          var bytes = new Uint8Array(buffer);
          var text = '';
          for (var b = 0; b < bytes.length; b++) { text += String.fromCharCode(bytes[b]); }
          cacheEntry.entries.push({
            url: requests[r].url,
            content_type: response.headers.get('content-type') || '',
            body_base64: btoa(text)
          });
        }
        out.cache_storage.push(cacheEntry);
      }
    }
  } catch (e) { out.cache_error = String(e); }
  return JSON.stringify(out);
})()`

// countPageStateJS counts what a page holds now, for a restore to be checked.
const countPageStateJS = `(async function(){
  var records = 0, entries = 0;
  try {
    if (typeof indexedDB !== 'undefined' && indexedDB.databases) {
      var infos = await indexedDB.databases();
      for (var i = 0; i < infos.length; i++) {
        var db = await new Promise(function(resolve, reject){
          var request = indexedDB.open(infos[i].name);
          request.onsuccess = function(){ resolve(request.result); };
          request.onerror = function(){ reject(request.error); };
        });
        var names = Array.prototype.slice.call(db.objectStoreNames);
        for (var s = 0; s < names.length; s++) {
          records += await new Promise(function(resolve, reject){
            var request = db.transaction(names[s], 'readonly').objectStore(names[s]).count();
            request.onsuccess = function(){ resolve(request.result); };
            request.onerror = function(){ reject(request.error); };
          });
        }
        db.close();
      }
    }
  } catch (e) {}
  try {
    if (typeof caches !== 'undefined') {
      var cacheNames = await caches.keys();
      for (var c = 0; c < cacheNames.length; c++) {
        var cache = await caches.open(cacheNames[c]);
        entries += (await cache.keys()).length;
      }
    }
  } catch (e) {}
  return JSON.stringify({records: records, entries: entries});
})()`

// writePageStateJS takes one argument, the state as JSON, and writes it back into
// the page. It is an expression, not an invoked function: WritePageState appends
// the call, so leaving a trailing () here would call the returned promise.
const writePageStateJS = `async function(stateJSON){
  var state = JSON.parse(stateJSON);
  var records = 0, entries = 0;
  for (var i = 0; i < (state.indexeddb || []).length; i++) {
    var database = state.indexeddb[i];
    var db = await new Promise(function(resolve, reject){
      var request = indexedDB.open(database.name, database.version || undefined);
      request.onupgradeneeded = function(){
        for (var s = 0; s < database.stores.length; s++) {
          var store = database.stores[s];
          if (!request.result.objectStoreNames.contains(store.name)) {
            request.result.createObjectStore(store.name, store.key_path ? {keyPath: store.key_path} : undefined);
          }
        }
      };
      request.onsuccess = function(){ resolve(request.result); };
      request.onerror = function(){ reject(request.error); };
    });
    for (var s = 0; s < database.stores.length; s++) {
      var store = database.stores[s];
      var tx = db.transaction(store.name, 'readwrite');
      var target = tx.objectStore(store.name);
      for (var r = 0; r < store.records.length; r++) {
        target.put(JSON.parse(store.records[r]));
        records++;
      }
      await new Promise(function(resolve){ tx.oncomplete = resolve; tx.onerror = resolve; tx.onabort = resolve; });
    }
    db.close();
  }
  for (var c = 0; c < (state.cache_storage || []).length; c++) {
    var cacheData = state.cache_storage[c];
    var cache = await caches.open(cacheData.name);
    for (var e = 0; e < cacheData.entries.length; e++) {
      var item = cacheData.entries[e];
      var raw = atob(item.body_base64 || '');
      var bytes = new Uint8Array(raw.length);
      for (var b = 0; b < raw.length; b++) { bytes[b] = raw.charCodeAt(b); }
      var headers = {};
      if (item.content_type) { headers['Content-Type'] = item.content_type; }
      await cache.put(new Request(item.url), new Response(bytes, {headers: headers}));
      entries++;
    }
  }
  return JSON.stringify({records: records, entries: entries});
}`

// PageState is what a page holds in the two surfaces that are not CDP-readable.
type PageState struct {
	IndexedDB []IDBDatabase  `json:"indexeddb,omitempty"`
	Cache     []CacheStorage `json:"cache_storage,omitempty"`

	IndexedDBError string `json:"indexeddb_error,omitempty"`
	CacheError     string `json:"cache_error,omitempty"`
	// CacheProbe records what the page said about the Cache Storage API itself,
	// so an empty surface can be told apart from an absent one.
	CacheProbe string `json:"cache_probe,omitempty"`
}

// ReadPageState reads IndexedDB and Cache Storage out of the session's page.
// A page that cannot answer returns an empty state: about:blank has neither
// surface, and that is not a failure.
func ReadPageState(ctx context.Context, session *browser.Session) (PageState, error) {
	if session == nil {
		return PageState{}, nil
	}
	text, err := evalRaw(ctx, session, readPageStateJS)
	if err != nil {
		return PageState{}, fmt.Errorf("page state: %w", err)
	}
	var state PageState
	if err := json.Unmarshal([]byte(text), &state); err != nil {
		return PageState{}, fmt.Errorf("page state: decode: %w", err)
	}
	return state, nil
}

// WritePageState puts the state back into the session's page. The page has to be
// at the origin already: both surfaces belong to it.
func WritePageState(ctx context.Context, session *browser.Session, state PageState) error {
	if session == nil || (len(state.IndexedDB) == 0 && len(state.Cache) == 0) {
		return nil
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("page state: %w", err)
	}
	expression := "(" + writePageStateJS + ")(" + strconv.Quote(string(payload)) + ")"
	if _, err := evalRaw(ctx, session, expression); err != nil {
		return fmt.Errorf("page state: %w", err)
	}
	return nil
}

// CountPageState reports how many records and entries a page holds now.
func CountPageState(ctx context.Context, session *browser.Session) (records int, entries int, err error) {
	if session == nil {
		return 0, 0, nil
	}
	text, err := evalRaw(ctx, session, countPageStateJS)
	if err != nil {
		return 0, 0, fmt.Errorf("page state: %w", err)
	}
	var counted struct {
		Records int `json:"records"`
		Entries int `json:"entries"`
	}
	if err := json.Unmarshal([]byte(text), &counted); err != nil {
		return 0, 0, fmt.Errorf("page state: decode: %w", err)
	}
	return counted.Records, counted.Entries, nil
}

// evalRaw evaluates an expression that answers with a JSON string and returns
// that string. An exception thrown by the page is an error here, the same way the
// seeder treats one: the alternative is a silent empty result.
func evalRaw(ctx context.Context, session *browser.Session, expression string) (string, error) {
	result, err := session.Do(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	})
	if err != nil {
		return "", err
	}
	var reply struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return "", fmt.Errorf("decode reply: %w", err)
	}
	if reply.ExceptionDetails != nil {
		detail := reply.ExceptionDetails.Text
		if reply.ExceptionDetails.Exception != nil && reply.ExceptionDetails.Exception.Description != "" {
			detail = reply.ExceptionDetails.Exception.Description
		}
		return "", fmt.Errorf("page threw: %s", detail)
	}
	// A string result arrives quoted.
	var text string
	if err := json.Unmarshal(reply.Result.Value, &text); err != nil {
		return "", fmt.Errorf("decode value: %w", err)
	}
	return text, nil
}
