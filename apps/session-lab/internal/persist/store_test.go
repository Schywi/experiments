package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWantsSelectsTheRequestedFraction(t *testing.T) {
	// Ids arrive as 1, 2, 3… so the selector has to hold the fraction on a short
	// run as well as a long one: `id % 100` gave 38 % of a 50-session run at a
	// 20 % rate, which is what this test exists to prevent.
	cases := []struct {
		rate  float64
		total int
		want  int
		tol   int
	}{
		{rate: 0, total: 50, want: 0, tol: 0},
		{rate: 0.2, total: 50, want: 10, tol: 4},
		{rate: 0.2, total: 1000, want: 200, tol: 20},
		{rate: 0.5, total: 1000, want: 500, tol: 30},
		{rate: 1, total: 50, want: 50, tol: 0},
	}
	for _, tc := range cases {
		dir, err := NewDir(Options{Dir: t.TempDir(), Rate: tc.rate})
		if err != nil {
			t.Fatalf("rate %v: %v", tc.rate, err)
		}
		got := 0
		for id := int64(1); id <= int64(tc.total); id++ {
			if dir.Wants(id) {
				got++
			}
		}
		if got < tc.want-tc.tol || got > tc.want+tc.tol {
			t.Errorf("rate %v over %d sessions: %d persist, want %d ±%d",
				tc.rate, tc.total, got, tc.want, tc.tol)
		}
	}
}

func TestWantsIsDeterministic(t *testing.T) {
	first, err := NewDir(Options{Dir: t.TempDir(), Rate: 0.2})
	if err != nil {
		t.Fatalf("new dir: %v", err)
	}
	second, err := NewDir(Options{Dir: t.TempDir(), Rate: 0.2})
	if err != nil {
		t.Fatalf("new dir: %v", err)
	}
	for id := int64(1); id <= 200; id++ {
		if first.Wants(id) != second.Wants(id) {
			t.Fatalf("session %d persists in one run and not the other", id)
		}
	}
}

func TestNewDirValidates(t *testing.T) {
	if _, err := NewDir(Options{}); err == nil {
		t.Error("a missing directory was accepted")
	}
	if _, err := NewDir(Options{Dir: t.TempDir(), Rate: 1.5}); err == nil {
		t.Error("rate 1.5 was accepted")
	}
	if _, err := NewDir(Options{Dir: t.TempDir(), Rate: 0.2, Budget: -1}); err == nil {
		t.Error("a negative budget was accepted")
	}
}

func TestRoundTripThroughGzip(t *testing.T) {
	blob := Blob{
		Cookies: []json.RawMessage{
			json.RawMessage(`{"name":"probe","value":"v1","domain":"example.com","path":"/","httpOnly":true}`),
		},
		Origin: "https://example.com",
		Local: map[string][]Item{
			"https://example.com": {{Name: "persist-probe", Value: "v1"}},
		},
	}
	raw, err := Encode(blob)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Cookies) != 1 || decoded.Origin != blob.Origin || decoded.Items() != 1 {
		t.Fatalf("decoded %+v, want one cookie, one item, origin %s", decoded, blob.Origin)
	}
	if got := decoded.Local["https://example.com"][0].Value; got != "v1" {
		t.Errorf("localStorage value %q, want v1", got)
	}
}

func TestDecodeRefusesGarbage(t *testing.T) {
	if _, err := Decode([]byte("not gzip")); err == nil {
		t.Error("garbage decoded without an error")
	}
}

func TestBudgetRefusesTheWrite(t *testing.T) {
	dir, err := NewDir(Options{Dir: t.TempDir(), Rate: 1, Budget: 100})
	if err != nil {
		t.Fatalf("new dir: %v", err)
	}
	// put, not commit: the defect this replaces was a budget that refused in the
	// counter and wrote the file anyway, and only put reaches the write.
	for id, size := range map[int64]int{1: 60, 2: 30, 3: 30} {
		if err := dir.put(id, make([]byte, size)); err != nil {
			t.Fatalf("put session %d: %v", id, err)
		}
	}
	stats := dir.Stats()
	if stats.Bytes != 90 {
		t.Errorf("booked %d bytes, want 90", stats.Bytes)
	}
	if stats.Refused != 1 {
		t.Errorf("%d refusals, want 1", stats.Refused)
	}
	if stats.Blobs != 2 {
		t.Errorf("%d blobs counted, want 2", stats.Blobs)
	}
	entries, err := os.ReadDir(dir.dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("%d files on disk, want 2: a refused blob was written anyway", len(entries))
	}
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", entry.Name(), err)
		}
		total += info.Size()
	}
	if total != 90 {
		t.Errorf("%d bytes on disk, want 90", total)
	}
}

func TestGenerationIncrements(t *testing.T) {
	dir, err := NewDir(Options{Dir: t.TempDir(), Rate: 1})
	if err != nil {
		t.Fatalf("new dir: %v", err)
	}
	if err := dir.put(7, []byte("first")); err != nil {
		t.Fatalf("put first: %v", err)
	}
	if err := dir.put(7, []byte("second")); err != nil {
		t.Fatalf("put second: %v", err)
	}
	entries, err := os.ReadDir(dir.dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("%d files, want 2: a second capture of one session overwrote the first", len(entries))
	}
	names := map[string]bool{}
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	for _, want := range []string{"000007-g1.json.gz", "000007-g2.json.gz"} {
		if !names[want] {
			t.Errorf("missing %s, got %v", want, names)
		}
	}
	if got := dir.Stats().Bytes; got != 11 {
		t.Errorf("booked %d bytes, want 11 (both generations)", got)
	}
}

func TestCommitAndRelease(t *testing.T) {
	dir, err := NewDir(Options{Dir: t.TempDir(), Rate: 1, Budget: 100})
	if err != nil {
		t.Fatalf("new dir: %v", err)
	}
	if !dir.commit(60) || !dir.commit(30) {
		t.Fatal("a booking inside the budget was refused")
	}
	if dir.commit(30) {
		t.Fatal("a booking that crosses the budget was accepted")
	}
	stats := dir.Stats()
	if stats.Bytes != 90 {
		t.Errorf("booked %d bytes, want 90", stats.Bytes)
	}
	if stats.Refused != 1 {
		t.Errorf("%d refusals, want 1", stats.Refused)
	}
	dir.release(60)
	if got := dir.Stats().Bytes; got != 30 {
		t.Errorf("after releasing 60, %d bytes booked, want 30", got)
	}
}

func TestWriteFileAtomicLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "000001-g1.json.gz")
	if err := writeFileAtomic(name, []byte("blob")); err != nil {
		t.Fatalf("write: %v", err)
	}
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(content) != "blob" {
		t.Errorf("content %q, want blob", content)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("%d files in the directory, want 1 (a .tmp was left behind)", len(entries))
	}
}
