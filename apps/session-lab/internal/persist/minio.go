package persist

// The object-store arm. Blobs go to MinIO over the S3 API; the writer tracks the
// bytes it has written in total, and keeps the bucket inside its budget by
// deleting the oldest blobs before it needs the room.
//
// MinIO runs in the cluster as config/session-lab/templates/minio.yaml. Any
// S3-compatible endpoint works: the client is behind objectStore, so the
// eviction order is testable without a server.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
)

// objectStore is the part of S3 the writer uses. *s3Client implements it for a
// real endpoint; tests implement it in memory.
type objectStore interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Remove(ctx context.Context, key string) error
}

// S3Options configures an S3 writer.
type S3Options struct {
	// Endpoint is host:port of the S3 endpoint, e.g. minio.session-lab:9000.
	Endpoint string
	// Secure selects TLS. The in-cluster MinIO talks plain HTTP on the cluster
	// network; an external endpoint usually needs TLS.
	Secure    bool
	AccessKey string
	SecretKey string
	Bucket    string
	// Prefix is prepended to every key, so one bucket can hold several runs:
	// "runs/<run-id>/".
	Prefix string
	// Rate is the fraction of sessions that persist: 0 none, 1 all.
	Rate float64
	// Budget is the most the bucket may hold, in compressed bytes. Zero means
	// unbounded. Oldest blobs are deleted to stay under it.
	Budget int64
	Log    *log.Logger
}

// S3 keeps blobs in a bucket and evicts the oldest to stay inside the budget.
type S3 struct {
	store  objectStore
	bucket string
	prefix string
	rate   float64
	budget int64
	log    *log.Logger

	mu      sync.Mutex
	gens    map[int64]int
	objects []Object
	bytes   int64
	ingest  int64
	evicted int64
	refused int
	failed  int
	blobs   int
}

// NewS3 connects to the endpoint and makes sure the bucket exists. Creating it
// is idempotent and keeps a first run from failing on a missing bucket.
func NewS3(ctx context.Context, opts S3Options) (*S3, error) {
	if opts.Endpoint == "" {
		return nil, fmt.Errorf("persist: no S3 endpoint given")
	}
	if opts.Bucket == "" {
		return nil, fmt.Errorf("persist: no S3 bucket given")
	}
	if opts.Rate < 0 || opts.Rate > 1 {
		return nil, fmt.Errorf("persist: rate %v is not between 0 and 1", opts.Rate)
	}
	if opts.Budget < 0 {
		return nil, fmt.Errorf("persist: budget %d is negative", opts.Budget)
	}
	client, err := minio.New(opts.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(opts.AccessKey, opts.SecretKey, ""),
		Secure: opts.Secure,
	})
	if err != nil {
		return nil, fmt.Errorf("persist: s3 client: %w", err)
	}
	exists, err := client.BucketExists(ctx, opts.Bucket)
	if err != nil {
		return nil, fmt.Errorf("persist: bucket %s: %w", opts.Bucket, err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, opts.Bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("persist: create bucket %s: %w", opts.Bucket, err)
		}
	}
	return NewS3WithStore(opts, &s3Client{client: client, bucket: opts.Bucket}), nil
}

// NewS3WithStore builds a writer over any objectStore. NewS3 calls it with a
// MinIO client; the tests call it with a map.
func NewS3WithStore(opts S3Options, store objectStore) *S3 {
	prefix := opts.Prefix
	if prefix != "" && prefix[len(prefix)-1] != '/' {
		prefix += "/"
	}
	return &S3{
		store: store, bucket: opts.Bucket, prefix: prefix,
		rate: opts.Rate, budget: opts.Budget, log: opts.Log,
		gens: map[int64]int{},
	}
}

// Wants reports whether this session persists.
func (s *S3) Wants(sessionID int64) bool {
	return wants(s.rate, sessionID)
}

// Capture snapshots the context and writes it to the bucket. A session that
// does not persist returns nil without touching the browser.
func (s *S3) Capture(ctx context.Context, conn *browser.Conn, session *browser.Session, id browser.ContextID, sessionID int64) error {
	if !s.Wants(sessionID) {
		return nil
	}
	started := time.Now()
	blob, err := Snapshot(ctx, conn, session, id)
	if err != nil {
		s.countFailure()
		return fmt.Errorf("capture: %w", err)
	}
	raw, err := Encode(blob)
	if err != nil {
		s.countFailure()
		return err
	}
	if err := s.Put(ctx, sessionID, raw); err != nil {
		return err
	}
	if s.log != nil {
		s.log.Printf("persist: session %d %d bytes (%d cookies, %d items) in %dms",
			sessionID, len(raw), len(blob.Cookies), blob.Items(),
			int64(time.Since(started)/time.Millisecond))
	}
	return nil
}

// Put stores one blob, deleting the oldest blobs first if the budget needs the
// room. The lock covers the evictions and the upload because one process owns
// the write path: that is the single-writer design in
// research/browser-session-persistence/01-persist-gateway-and-budget.md, and it
// is why there is no reserve/commit dance here.
func (s *S3) Put(ctx context.Context, sessionID int64, raw []byte) error {
	size := int64(len(raw))
	s.mu.Lock()
	for s.budget > 0 && s.bytes+size > s.budget && len(s.objects) > 0 {
		victim := s.objects[0]
		if err := s.store.Remove(ctx, victim.Key); err != nil {
			s.mu.Unlock()
			s.countFailure()
			return fmt.Errorf("evict %s: %w", victim.Key, err)
		}
		s.objects = s.objects[1:]
		s.bytes -= victim.Bytes
		s.evicted += victim.Bytes
		if s.log != nil {
			s.log.Printf("persist: evicted %s (%d bytes), %d of %d retained",
				victim.Key, victim.Bytes, s.bytes, s.budget)
		}
	}
	if s.budget > 0 && s.bytes+size > s.budget {
		// Nothing left to evict and the blob still does not fit: one blob is
		// bigger than the budget. Count it and keep the run going.
		s.refused++
		s.mu.Unlock()
		if s.log != nil {
			s.log.Printf("persist: refused %d bytes, one blob exceeds the %d byte budget", size, s.budget)
		}
		return nil
	}
	s.gens[sessionID]++
	key := fmt.Sprintf("%s%06d-g%d.json.gz", s.prefix, sessionID, s.gens[sessionID])
	s.bytes += size
	s.ingest += size
	s.mu.Unlock()

	if err := s.store.Put(ctx, key, raw); err != nil {
		s.mu.Lock()
		s.bytes -= size
		s.ingest -= size
		s.mu.Unlock()
		s.countFailure()
		return fmt.Errorf("put %s: %w", key, err)
	}

	s.mu.Lock()
	s.objects = append(s.objects, Object{Key: key, Bytes: size, At: time.Now()})
	s.blobs++
	s.mu.Unlock()
	return nil
}

// Get reads one stored blob.
func (s *S3) Get(ctx context.Context, key string) (Blob, error) {
	raw, err := s.store.Get(ctx, key)
	if err != nil {
		return Blob{}, fmt.Errorf("get %s: %w", key, err)
	}
	return Decode(raw)
}

// Objects lists the blobs held now, oldest first.
func (s *S3) Objects() []Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Object, len(s.objects))
	copy(out, s.objects)
	return out
}

// Stats reports the arm's totals.
func (s *S3) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := Stats{
		Rate: s.rate, Budget: s.budget, Blobs: s.blobs, Bytes: s.bytes,
		IngestBytes: s.ingest, EvictedBytes: s.evicted,
		Refused: s.refused, Failed: s.failed,
	}
	if s.blobs > 0 {
		stats.MeanBytes = float64(s.ingest) / float64(s.blobs)
	}
	return stats
}

// String describes the arm for a log line.
func (s *S3) String() string {
	return fmt.Sprintf("persist: s3 bucket %s prefix %q at %v, rate %.0f%%, budget %d bytes",
		s.bucket, s.prefix, s.store, s.rate*100, s.budget)
}

func (s *S3) countFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed++
}

// s3Client is objectStore over a MinIO client. Blobs are small, so they are read
// into memory rather than streamed.
type s3Client struct {
	client *minio.Client
	bucket string
}

func (c *s3Client) Put(ctx context.Context, key string, data []byte) error {
	_, err := c.client.PutObject(ctx, c.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/gzip"})
	return err
}

func (c *s3Client) Get(ctx context.Context, key string) ([]byte, error) {
	object, err := c.client.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer object.Close()
	// GetObject is lazy: a missing key surfaces here, on the read.
	return io.ReadAll(object)
}

func (c *s3Client) Remove(ctx context.Context, key string) error {
	return c.client.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{})
}

func (c *s3Client) String() string { return "s3://" + c.bucket }

// wants is the shared selector, so both arms pick the same sessions at one rate.
func wants(rate float64, sessionID int64) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	return spread(sessionID) < int64(rate*100+0.5)
}
