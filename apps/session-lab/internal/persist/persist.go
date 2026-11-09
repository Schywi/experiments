// Package persist writes a session's restorable browser state to MinIO.
//
// One blob per persisted session, gzipped, at runs/<run-id>/<session>-g<n>.json.gz.
// The arm counts every byte it has written (ingest) and keeps the bucket inside
// --persist-budget by deleting the oldest blobs before a write that would cross
// it. What goes into a blob is Chromium's restorable state for one browser
// context — cookies and localStorage — and nothing above this package parses it.
//
// MinIO is the only store. The runner reads its endpoint and credentials from the
// cluster it is already talking to, so there is no flag to point it anywhere
// else and no second destination to fall back to.
package persist

import (
	"context"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
)

// Writer is what the runner calls before it disposes a session's context. It
// decides whether a session persists; the runner does not.
type Writer interface {
	Capture(ctx context.Context, conn *browser.Conn, session *browser.Session, id browser.ContextID, sessionID int64) error
}

// Store is a Writer whose blobs can be read back and listed, which is what the
// restore arm needs.
type Store interface {
	Writer
	// Get returns one stored blob by the key Objects reported.
	Get(ctx context.Context, key string) (Blob, error)
	// Objects lists the blobs held now, oldest first.
	Objects() []Object
	// Stats reports the arm's counters.
	Stats() Stats
}

// Object is one stored blob.
type Object struct {
	Key   string
	Bytes int64
	At    time.Time
}

// Stats is what a run writes down about its persistence arm.
type Stats struct {
	Rate   float64 `json:"rate"`
	Budget int64   `json:"budget"`
	Blobs  int     `json:"blobs"`
	// Bytes is what the bucket holds now.
	Bytes int64 `json:"bytes"`
	// IngestBytes is every byte written over the run, cumulative. Eviction does
	// not reduce it, so this is the total GB the arm dealt in.
	IngestBytes int64 `json:"ingest_bytes"`
	// EvictedBytes is what was deleted to stay inside the budget.
	EvictedBytes int64   `json:"evicted_bytes"`
	Refused      int     `json:"refused"`
	Failed       int     `json:"failed"`
	MeanBytes    float64 `json:"mean_bytes"`
	// Datasets breaks the arm down by the class of state each session carried, so
	// a run over a mix is readable instead of one aggregate.
	Datasets []DatasetStats `json:"datasets,omitempty"`
}

// wants reports whether this session persists at the configured rate.
func wants(rate float64, sessionID int64) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	return spread(sessionID) < int64(rate*100+0.5)
}

// spread scatters consecutive session ids across 0..99. Ids arrive as 1, 2, 3…
// so `id % 100` makes a 20 % rate select ids 1..19 of a 50-session run: 38 % of
// what the operator asked for. Hashing the id keeps the fraction the same for a
// short run and a long one, and stays deterministic so two runs at one rate
// persist the same sessions.
func spread(id int64) int64 {
	x := uint64(id) * 2654435761
	x ^= x >> 13
	x *= 2654435761
	return int64(x % 100)
}
