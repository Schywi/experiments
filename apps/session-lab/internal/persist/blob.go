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
