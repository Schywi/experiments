package persist

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
)

// Writer is what the runner calls before it disposes a session's context. It
// decides whether a session persists; the runner does not.
type Writer interface {
	Capture(ctx context.Context, conn *browser.Conn, session *browser.Session, id browser.ContextID, sessionID int64) error
}

// Store is a Writer whose blobs can be read back and listed, which is what the
// restore arm needs. Dir and S3 both implement it.
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
	Rate   float64 `json:"rate"`
	Budget int64   `json:"budget"`
	Blobs  int     `json:"blobs"`
	// Bytes is what the store holds now.
	Bytes int64 `json:"bytes"`
	// IngestBytes is every byte written over the run, cumulative. Eviction does
	// not reduce it, so this is the total GB the arm dealt in.
	IngestBytes int64 `json:"ingest_bytes"`
	// EvictedBytes is what was deleted to stay inside the budget.
	EvictedBytes int64   `json:"evicted_bytes"`
	Refused      int     `json:"refused"`
	Failed       int     `json:"failed"`
	MeanBytes    float64 `json:"mean_bytes"`
}

// Dir keeps blobs as one gzipped file per session in a local directory.
//
// The ledger and the directory are the same thing: a capture books its bytes
// under the mutex before it writes, so two concurrent captures cannot both see
// room for themselves, and a blob that would cross the budget never reaches the
// disk.
type Dir struct {
	mu      sync.Mutex
	dir     string
	rate    float64
	budget  int64
	log     *log.Logger
	blobs   int
	bytes   int64
	ingest  int64
	refused int
	failed  int
	// gens counts captures per session, so a session that persists twice keeps
	// both blobs instead of overwriting the first.
	gens map[int64]int
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
	return &Dir{dir: opts.Dir, rate: opts.Rate, budget: opts.Budget, log: opts.Log,
		gens: map[int64]int{}}, nil
}

// Wants reports whether this session persists.
func (d *Dir) Wants(sessionID int64) bool { return wants(d.rate, sessionID) }

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
	if err := d.put(sessionID, raw); err != nil {
		return err
	}

	if d.log != nil {
		d.log.Printf("persist: session %d %d bytes (%d cookies, %d items) in %dms",
			sessionID, len(raw), len(blob.Cookies), blob.Items(),
			int64(time.Since(started)/time.Millisecond))
	}
	return nil
}

// put books the bytes, writes the blob and counts it. A blob that would cross
// the budget is refused before it reaches the disk: the budget bounds the
// directory, and a counter that refuses after the write bounds nothing.
func (d *Dir) put(sessionID int64, raw []byte) error {
	size := int64(len(raw))
	if !d.commit(size) {
		return nil
	}
	name := filepath.Join(d.dir, d.nextName(sessionID))
	if err := writeFileAtomic(name, raw); err != nil {
		d.release(size)
		d.countFailure()
		return fmt.Errorf("capture: write %s: %w", name, err)
	}
	d.mu.Lock()
	d.blobs++
	d.ingest += size
	d.mu.Unlock()
	return nil
}

// nextName is <id>-g<n>, n counting the captures of that session so a second
// capture does not overwrite the first.
func (d *Dir) nextName(sessionID int64) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gens[sessionID]++
	return fmt.Sprintf("%06d-g%d.json.gz", sessionID, d.gens[sessionID])
}

// Stats reports the arm's totals.
func (d *Dir) Stats() Stats {
	d.mu.Lock()
	defer d.mu.Unlock()
	stats := Stats{
		Rate: d.rate, Budget: d.budget, Blobs: d.blobs, Bytes: d.bytes,
		IngestBytes: d.ingest, Refused: d.refused, Failed: d.failed,
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

// Get reads one stored blob.
func (d *Dir) Get(ctx context.Context, key string) (Blob, error) {
	raw, err := os.ReadFile(filepath.Join(d.dir, key))
	if err != nil {
		return Blob{}, err
	}
	return Decode(raw)
}

// Objects lists the blobs in the directory, oldest first by modification time.
func (d *Dir) Objects() []Object {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil
	}
	out := make([]Object, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json.gz") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, Object{Key: entry.Name(), Bytes: info.Size(), At: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// commit books bytes against the budget before the write happens. It reports
// whether they were booked: false means the blob would cross the budget and the
// caller must not write it. A full budget is a capacity outcome, not a failed
// session, which is why the refusal is not an error.
func (d *Dir) commit(size int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.budget > 0 && d.bytes+size > d.budget {
		d.refused++
		if d.log != nil {
			d.log.Printf("persist: refused %d bytes, %d of %d booked", size, d.bytes, d.budget)
		}
		return false
	}
	d.bytes += size
	return true
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
