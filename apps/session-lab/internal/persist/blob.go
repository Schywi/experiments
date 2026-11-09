// Package persist captures one browser context's restorable state and puts it
// back into another context.
//
// The unit is a browser context, not a profile. Eight contexts share one
// Chromium, so there is no per-session directory to copy and copying a live
// profile would be unsafe anyway; capture goes through the DevTools connection
// the runtime already holds, over the CDP calls in capture.go.
//
// A blob holds cookies, localStorage per origin and the page's own URL. It does
// not hold the DOM, the HTTP cache, service workers, sessionStorage across a
// navigation, IndexedDB, or anything in the JavaScript heap: a restored session
// reloads its page and re-authenticates.
//
// Blobs go to MinIO through the S3 writer in minio.go; this file is only the
// format.
package persist

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
)

// Version is the blob format this package writes. A blob carrying any other
// version is refused rather than half-restored.
const Version = 1

// Item is one localStorage key and its value.
type Item struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Blob is one context's restorable state.
//
// Cookies are kept as the raw JSON Chromium returned for each one, so a restore
// replays exactly the fields a capture saw and a Chromium that adds a field does
// not lose it here.
type Blob struct {
	Version int `json:"version"`
	// Origin is the page's security origin: the key localStorage is filed under.
	Origin string `json:"origin,omitempty"`
	// URL is the page the session was on, so a restore lands on the same page
	// and not just the same site. A blob without it restores to Origin.
	URL     string            `json:"url,omitempty"`
	Cookies []json.RawMessage `json:"cookies,omitempty"`
	Local   map[string][]Item `json:"local_storage,omitempty"`
	// IndexedDB and Cache hold the two surfaces that are read and written through
	// the page itself: IndexedDB has a CDP domain that only reads, and Cache
	// Storage has none at all. The two error fields record why a surface came back
	// empty, so an unreadable surface is visible in the blob.
	IndexedDB []IDBDatabase  `json:"indexeddb,omitempty"`
	Cache     []CacheStorage `json:"cache_storage,omitempty"`

	// IndexedDBError and CacheError are what the page reported when it could not
	// be read. Empty means the surface was read and held nothing. CacheProbe says
	// what the page made of the Cache Storage API, so an empty surface can be told
	// apart from one that was never there.
	IndexedDBError string `json:"indexeddb_error,omitempty"`
	CacheError     string `json:"cache_error,omitempty"`
	CacheProbe     string `json:"cache_probe,omitempty"`
}

// IDBDatabase is one IndexedDB database: its stores and their records.
type IDBDatabase struct {
	Name    string     `json:"name"`
	Version int64      `json:"version,omitempty"`
	Stores  []IDBStore `json:"stores"`
}

// IDBStore is one object store. Records are kept as the JSON text the page
// returned, so a restore writes back exactly what the page held.
type IDBStore struct {
	Name    string   `json:"name"`
	KeyPath string   `json:"key_path,omitempty"`
	Records []string `json:"records"`
}

// CacheStorage is one Cache Storage cache and its entries.
type CacheStorage struct {
	Name    string       `json:"name"`
	Entries []CacheEntry `json:"entries"`
}

// CacheEntry is one cached response. The body is base64 because a response body
// is bytes.
type CacheEntry struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type,omitempty"`
	BodyB64     string `json:"body_base64,omitempty"`
}

// IDBRecords reports how many IndexedDB records the blob holds.
func (b Blob) IDBRecords() int {
	total := 0
	for _, database := range b.IndexedDB {
		for _, store := range database.Stores {
			total += len(store.Records)
		}
	}
	return total
}

// CacheEntries reports how many Cache Storage entries the blob holds.
func (b Blob) CacheEntries() int {
	total := 0
	for _, cache := range b.Cache {
		total += len(cache.Entries)
	}
	return total
}

// Items reports how many localStorage entries the blob holds, across origins.
func (b Blob) Items() int {
	total := 0
	for _, items := range b.Local {
		total += len(items)
	}
	return total
}

// Encode serialises and gzips a blob. One blob is one file or one object, and a
// JSON document of cookies and storage compresses about four to one.
func Encode(blob Blob) ([]byte, error) {
	blob.Version = Version
	payload, err := json.Marshal(blob)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(payload); err != nil {
		writer.Close()
		return nil, fmt.Errorf("encode: gzip: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("encode: gzip close: %w", err)
	}
	return buffer.Bytes(), nil
}

// Decode reverses Encode and refuses a format it does not know.
func Decode(raw []byte) (Blob, error) {
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return Blob{}, fmt.Errorf("decode: gzip: %w", err)
	}
	defer reader.Close()
	payload, err := io.ReadAll(reader)
	if err != nil {
		return Blob{}, fmt.Errorf("decode: read: %w", err)
	}
	var blob Blob
	if err := json.Unmarshal(payload, &blob); err != nil {
		return Blob{}, fmt.Errorf("decode: json: %w", err)
	}
	if blob.Version != Version {
		return Blob{}, fmt.Errorf("decode: blob version %d, this build writes %d", blob.Version, Version)
	}
	return blob, nil
}
