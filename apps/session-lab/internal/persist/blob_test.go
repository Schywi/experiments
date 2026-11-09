package persist

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBlobCarriesThePageURL(t *testing.T) {
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

func TestBlobCarriesIndexedDBAndCache(t *testing.T) {
	blob := Blob{
		Origin: "https://example.com",
		URL:    "https://example.com/",
		IndexedDB: []IDBDatabase{{
			Name: "seed-db", Version: 1,
			Stores: []IDBStore{{
				Name: "items", KeyPath: "id",
				Records: []string{`{"id":0,"blob":"abc"}`, `{"id":1,"blob":"def"}`},
			}},
		}},
		Cache: []CacheStorage{{
			Name: "seed-cache",
			Entries: []CacheEntry{{
				URL: "https://example.com/seed-0", ContentType: "text/plain", BodyB64: "aGVsbG8=",
			}},
		}},
	}
	raw, err := Encode(blob)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.IDBRecords() != 2 {
		t.Errorf("%d IndexedDB records came back, want 2", decoded.IDBRecords())
	}
	if decoded.CacheEntries() != 1 {
		t.Errorf("%d cache entries came back, want 1", decoded.CacheEntries())
	}
	if got := decoded.IndexedDB[0].Stores[0].KeyPath; got != "id" {
		t.Errorf("key path %q, want id", got)
	}
	if got := decoded.Cache[0].Entries[0].BodyB64; got != "aGVsbG8=" {
		t.Errorf("cache body %q, want the base64 that went in", got)
	}
}

func TestReadPageStateScriptsAreWhole(t *testing.T) {
	// Cheap structural checks on the three scripts: the parts that would silently
	// do nothing if they were wrong.
	for _, want := range []string{"indexedDB.databases()", "getAll()", "caches.keys()", "body_base64"} {
		if !strings.Contains(readPageStateJS, want) {
			t.Errorf("read script is missing %q", want)
		}
	}
	for _, want := range []string{"onupgradeneeded", "createObjectStore", "cache.put("} {
		if !strings.Contains(writePageStateJS, want) {
			t.Errorf("write script is missing %q", want)
		}
	}
	for _, want := range []string{"objectStore(", ".count()", "caches.keys()"} {
		if !strings.Contains(countPageStateJS, want) {
			t.Errorf("count script is missing %q", want)
		}
	}
}

func TestVerifiedCompleteOnlyWhenEverySurfaceCameBack(t *testing.T) {
	full := Verified{Cookies: 2, CookiesWanted: 2, Items: 4, ItemsWanted: 4, Records: 8, RecordsWanted: 8}
	if !full.Complete() {
		t.Error("a fully restored blob reported incomplete")
	}
	missingIDB := full
	missingIDB.Records = 0
	if missingIDB.Complete() {
		t.Error("a blob that lost its IndexedDB records reported complete")
	}
	noSurfaces := Verified{}
	if !noSurfaces.Complete() {
		t.Error("a blob with nothing in it reported incomplete")
	}
}
