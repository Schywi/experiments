package persist

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
)

// fakeStore is the object store without a server, so the eviction order and the
// counters are testable.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	removed []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: map[string][]byte{}}
}

func (f *fakeStore) Put(_ context.Context, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = append([]byte(nil), data...)
	return nil
}

func (f *fakeStore) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[key]
	if !ok {
		return nil, fmt.Errorf("no such key %s", key)
	}
	return data, nil
}

func (f *fakeStore) Remove(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	f.removed = append(f.removed, key)
	return nil
}

func (f *fakeStore) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.objects))
	for key := range f.objects {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func newTestS3(t *testing.T, budget int64) (*S3, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	return NewS3WithStore(S3Options{
		Bucket: "sessions", Prefix: "runs/r1", Rate: 1, Budget: budget,
	}, store), store
}

// TestS3EvictsTheOldestFirst is the behaviour the bucket budget exists for: the
// blob that leaves is the one written first, and the bytes it held stop counting
// against what is retained.
func TestS3EvictsTheOldestFirst(t *testing.T) {
	ctx := context.Background()
	writer, store := newTestS3(t, 100)

	for id, size := range map[int64]int{1: 60, 2: 30} {
		if err := writer.Put(ctx, id, make([]byte, size)); err != nil {
			t.Fatalf("put %d: %v", id, err)
		}
	}
	// The third blob only fits if the first one is evicted: 60+30+30 > 100 and
	// 30+30 <= 100.
	if err := writer.Put(ctx, 3, make([]byte, 30)); err != nil {
		t.Fatalf("put 3: %v", err)
	}

	stats := writer.Stats()
	if stats.Bytes != 60 {
		t.Errorf("%d bytes retained, want 60", stats.Bytes)
	}
	if stats.EvictedBytes != 60 {
		t.Errorf("%d bytes evicted, want 60", stats.EvictedBytes)
	}
	if stats.IngestBytes != 120 {
		t.Errorf("%d bytes ingested, want 120 (eviction does not reduce it)", stats.IngestBytes)
	}
	if stats.Refused != 0 {
		t.Errorf("%d refusals, want 0: eviction should have made room", stats.Refused)
	}
	keys := store.keys()
	want := []string{"runs/r1/000002-g1.json.gz", "runs/r1/000003-g1.json.gz"}
	if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
		t.Errorf("bucket holds %v, want %v", keys, want)
	}
	if len(store.removed) != 1 || store.removed[0] != "runs/r1/000001-g1.json.gz" {
		t.Errorf("removed %v, want the first blob", store.removed)
	}
}

// TestS3RefusesOneBlobOverBudget covers the case eviction cannot solve: nothing
// is stored, the refusal is counted, and the run continues.
func TestS3RefusesOneBlobOverBudget(t *testing.T) {
	ctx := context.Background()
	writer, store := newTestS3(t, 10)

	if err := writer.Put(ctx, 1, make([]byte, 50)); err != nil {
		t.Fatalf("put: %v", err)
	}
	stats := writer.Stats()
	if stats.Refused != 1 {
		t.Errorf("%d refusals, want 1", stats.Refused)
	}
	if stats.Bytes != 0 || stats.IngestBytes != 0 {
		t.Errorf("%d bytes retained and %d ingested, want 0 and 0", stats.Bytes, stats.IngestBytes)
	}
	if len(store.keys()) != 0 {
		t.Errorf("bucket holds %v, want nothing", store.keys())
	}
}

func TestS3GetReadsBackABlob(t *testing.T) {
	ctx := context.Background()
	writer, _ := newTestS3(t, 0)

	raw, err := Encode(Blob{
		Origin: "https://example.com",
		Cookies: []json.RawMessage{
			json.RawMessage(`{"name":"probe","value":"v1"}`),
		},
		Local: map[string][]Item{
			"https://example.com": {{Name: "probe", Value: "v1"}},
		},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := writer.Put(ctx, 7, raw); err != nil {
		t.Fatalf("put: %v", err)
	}

	objects := writer.Objects()
	if len(objects) != 1 {
		t.Fatalf("%d objects listed, want 1", len(objects))
	}
	blob, err := writer.Get(ctx, objects[0].Key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if blob.Origin != "https://example.com" || blob.Items() != 1 || len(blob.Cookies) != 1 {
		t.Errorf("read back %+v, want one cookie, one item at example.com", blob)
	}
}

func TestS3WantsMatchesTheLocalArm(t *testing.T) {
	dir, err := NewDir(Options{Dir: t.TempDir(), Rate: 0.2})
	if err != nil {
		t.Fatalf("new dir: %v", err)
	}
	writer, _ := newTestS3WithRate(t, 0.2)
	for id := int64(1); id <= 200; id++ {
		if dir.Wants(id) != writer.Wants(id) {
			t.Fatalf("session %d: the two arms disagree on whether it persists", id)
		}
	}
}

func newTestS3WithRate(t *testing.T, rate float64) (*S3, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	return NewS3WithStore(S3Options{Bucket: "sessions", Rate: rate}, store), store
}
