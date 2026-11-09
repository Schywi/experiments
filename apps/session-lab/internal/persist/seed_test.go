package persist

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSeedScriptWritesTheRequestedKeysAndSize(t *testing.T) {
	script := seedScript(7, 65536, 4, 3)
	for _, want := range []string{
		"localStorage.setItem('seed-'", // the keys
		"var per=16384, keys=4",        // 65536 spread over 4 keys
		"var cookies=3",
		"document.cookie=",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("seed script is missing %q", want)
		}
	}
}

func TestSeedScriptIsDeterministicPerSession(t *testing.T) {
	// Two runs at one seed size have to write the same bytes, or the two arms
	// are not comparable.
	if a, b := seedScript(11, 1024, 2, 0), seedScript(11, 1024, 2, 0); a != b {
		t.Error("the same session produced two different seed scripts")
	}
	if a, b := seedScript(11, 1024, 2, 0), seedScript(12, 1024, 2, 0); a == b {
		t.Error("two sessions produced the same seed script; the state would not vary")
	}
}

func TestSeedZeroWritesNothing(t *testing.T) {
	seed := Seed{}
	if seed.Total() != 0 {
		t.Errorf("an empty Seed reports %d bytes of work", seed.Total())
	}
	if err := seed.Seed(nil, nil, 1); err != nil {
		t.Errorf("an empty Seed returned %v, want nil without touching the browser", err)
	}
	if got := seed.Keys(); got != 1 {
		t.Errorf("%d keys for a zero Seed, want 1", got)
	}
}

func TestBlobCarriesItsURL(t *testing.T) {
	// The page URL, not just the origin: a restore has to land on the same page.
	blob := Blob{
		Origin: "https://universe.leagueoflegends.com",
		URL:    "https://universe.leagueoflegends.com/pt_BR/champion/akali/",
		Cookies: []json.RawMessage{
			json.RawMessage(`{"name":"probe","value":"v1"}`),
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
	if decoded.URL != blob.URL {
		t.Errorf("URL %q, want %q", decoded.URL, blob.URL)
	}
	if decoded.Origin != blob.Origin {
		t.Errorf("origin %q, want %q", decoded.Origin, blob.Origin)
	}
}
