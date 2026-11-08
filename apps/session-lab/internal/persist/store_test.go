package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWantsIsDeterministic(t *testing.T) {
	cases := []struct {
		rate float64
		want int // sessions out of 0..99 that should persist
	}{
		{rate: 0, want: 0},
		{rate: 0.01, want: 1},
		{rate: 0.2, want: 20},
		{rate: 0.5, want: 50},
		{rate: 1, want: 100},
	}
	for _, tc := range cases {
		dir, err := NewDir(Options{Dir: t.TempDir(), Rate: tc.rate})
		if err != nil {
			t.Fatalf("rate %v: %v", tc.rate, err)
		}
		got := 0
		for id := int64(0); id < 100; id++ {
			if dir.Wants(id) {
				got++
			}
		}
		if got != tc.want {
			t.Errorf("rate %v: %d of 100 sessions persist, want %d", tc.rate, got, tc.want)
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

func TestBudgetIsReservedNotOverrun(t *testing.T) {
	dir, err := NewDir(Options{Dir: t.TempDir(), Rate: 1, Budget: 100})
	if err != nil {
		t.Fatalf("new dir: %v", err)
	}
	dir.reserve(60)
	dir.reserve(30)
	dir.reserve(30) // would cross the budget
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
