package persist

import (
	"encoding/json"
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
		got := 0
		for id := int64(1); id <= int64(tc.total); id++ {
			if wants(tc.rate, id) {
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
	for id := int64(1); id <= 200; id++ {
		first, second := wants(0.2, id), wants(0.2, id)
		if first != second {
			t.Fatalf("session %d settled differently on two calls", id)
		}
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
