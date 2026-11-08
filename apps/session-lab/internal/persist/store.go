package persist

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
)

// Writer is what the runner calls before it disposes a session's context. It
// decides whether a session persists; the runner does not.
type Writer interface {
	Capture(ctx context.Context, conn *browser.Conn, session *browser.Session, id browser.ContextID, sessionID int64) error
}

// Options configures a Dir.
type Options struct {
	// Dir is where blobs are written. Created if missing.
	Dir string
	// Rate is the fraction of sessions that persist: 0 none, 1 all, 0.2 every
	// session whose id is a multiple of five. The choice is deterministic so
	// two runs at the same rate persist the same sessions.
	Rate float64
	// Budget is the most this directory may hold in compressed bytes. Zero
	// means unbounded. A blob that would cross it is refused and counted, which
	// keeps a load run from filling the node's disk.
	Budget int64
	// Log receives one line per capture. Optional.
	Log *log.Logger
}

// Stats is what a run writes down about its persistence arm.
type Stats struct {
	Rate      float64 `json:"rate"`
	Budget    int64   `json:"budget"`
	Blobs     int     `json:"blobs"`
	Bytes     int64   `json:"bytes"`
	Refused   int     `json:"refused"`
	Failed    int     `json:"failed"`
	MeanBytes float64 `json:"mean_bytes"`
}

// Dir keeps blobs as one gzipped file per session in a local directory.
//
// The ledger and the directory are the same thing: a capture reserves its bytes
// under the mutex before it writes, so two concurrent captures cannot both see
// room for themselves.
type Dir struct {
	mu      sync.Mutex
	dir     string
	rate    float64
	budget  int64
	log     *log.Logger
	blobs   int
	bytes   int64
	refused int
	failed  int
}

// NewDir validates the options and creates the directory.
func NewDir(opts Options) (*Dir, error) {
	if opts.Dir == "" {
		return nil, fmt.Errorf("persist: no directory given")
	}
	if opts.Rate < 0 || opts.Rate > 1 {
		return nil, fmt.Errorf("persist: rate %v is not between 0 and 1", opts.Rate)
	}
	if opts.Budget < 0 {
		return nil, fmt.Errorf("persist: budget %d is negative", opts.Budget)
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("persist: %w", err)
	}
	return &Dir{dir: opts.Dir, rate: opts.Rate, budget: opts.Budget, log: opts.Log}, nil
}

// Wants reports whether this session persists.
func (d *Dir) Wants(sessionID int64) bool {
	if d.rate <= 0 {
		return false
	}
	if d.rate >= 1 {
		return true
	}
	return sessionID%100 < int64(math.Round(d.rate*100))
}

// Capture snapshots the context and writes it to the directory. A session that
// does not persist returns nil without touching the browser.
func (d *Dir) Capture(ctx context.Context, conn *browser.Conn, session *browser.Session, id browser.ContextID, sessionID int64) error {
	if !d.Wants(sessionID) {
		return nil
	}
	started := time.Now()
	blob, err := Snapshot(ctx, conn, session, id)
	if err != nil {
		d.countFailure()
		return fmt.Errorf("capture: %w", err)
	}
	raw, err := Encode(blob)
	if err != nil {
		d.countFailure()
		return err
	}

	name := filepath.Join(d.dir, fmt.Sprintf("%06d-g%d.json.gz", sessionID, 1))
	d.reserve(int64(len(raw)))
	if err := writeFileAtomic(name, raw); err != nil {
		d.release(int64(len(raw)))
		d.countFailure()
		return fmt.Errorf("capture: write %s: %w", name, err)
	}
	d.mu.Lock()
	d.blobs++
	d.mu.Unlock()

	if d.log != nil {
		d.log.Printf("persist: session %d %d bytes (%d cookies, %d items) in %dms",
			sessionID, len(raw), len(blob.Cookies), blob.Items(),
			int64(time.Since(started)/time.Millisecond))
	}
	return nil
}

// Stats reports the arm's totals.
func (d *Dir) Stats() Stats {
	d.mu.Lock()
	defer d.mu.Unlock()
	stats := Stats{
		Rate: d.rate, Budget: d.budget, Blobs: d.blobs, Bytes: d.bytes,
		Refused: d.refused, Failed: d.failed,
	}
	if d.blobs > 0 {
		stats.MeanBytes = float64(d.bytes) / float64(d.blobs)
	}
	return stats
}

// String describes the arm for a log line.
func (d *Dir) String() string {
	return fmt.Sprintf("persist: directory %s, rate %.0f%%, budget %d bytes",
		d.dir, d.rate*100, d.budget)
}

// reserve books bytes against the budget before the write happens. A blob that
// would cross the budget is refused and counted, and the caller still returns
// without an error: a full budget is a capacity outcome, not a failed session.
func (d *Dir) reserve(size int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.budget > 0 && d.bytes+size > d.budget {
		d.refused++
		if d.log != nil {
			d.log.Printf("persist: refused %d bytes, %d of %d used", size, d.bytes, d.budget)
		}
		return
	}
	d.bytes += size
}

// release returns bytes a failed write had booked.
func (d *Dir) release(size int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.bytes -= size
	if d.bytes < 0 {
		d.bytes = 0
	}
}

func (d *Dir) countFailure() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failed++
}

// writeFileAtomic writes through a temporary name and renames, so a reader never
// sees half a blob.
func writeFileAtomic(name string, raw []byte) error {
	temp := name + ".tmp"
	if err := os.WriteFile(temp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(temp, name)
}
