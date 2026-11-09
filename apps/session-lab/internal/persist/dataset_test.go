package persist

import (
	"strings"
	"testing"
)

func TestParsePlanReadsLabelsAndWeights(t *testing.T) {
	plan, err := ParsePlan("local-64k:10,idb-256k:3,cache-1m:1,cookies-4:5,none:2")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"local-64k", "idb-256k", "cache-1m", "cookies-4", "none"}
	got := plan.Labels()
	if len(got) != len(want) {
		t.Fatalf("labels %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("label %d is %q, want %q", i, got[i], want[i])
		}
	}
	local, err := ParseDataset("local-256k")
	if err != nil {
		t.Fatalf("parse dataset: %v", err)
	}
	if local.Bytes != 256*1024 || local.Kind != KindLocal {
		t.Errorf("local-256k parsed as %+v", local)
	}
	oneMeg, err := ParseDataset("cache-1m")
	if err != nil {
		t.Fatalf("parse dataset: %v", err)
	}
	if oneMeg.Bytes != 1024*1024 || oneMeg.Kind != KindCache {
		t.Errorf("cache-1m parsed as %+v", oneMeg)
	}
	cookies, err := ParseDataset("cookies-4")
	if err != nil {
		t.Fatalf("parse dataset: %v", err)
	}
	if cookies.Count != 4 || cookies.Kind != KindCookies {
		t.Errorf("cookies-4 parsed as %+v", cookies)
	}
}

func TestParsePlanRefusesWhatItCannotRead(t *testing.T) {
	for _, spec := range []string{
		"local-64k",         // no weight
		"local-64k:many",    // weight is not a number
		"nonsense-64k:1",    // unknown surface
		"local:1",           // no size
		"cookies:4",         // cookies needs a count in the label
		"none-4k:1",         // none takes no size
		"local-64k:0",       // carries no sessions
		"",                  // empty
	} {
		if _, err := ParsePlan(spec); err == nil {
			t.Errorf("ParsePlan(%q) accepted a spec it cannot read", spec)
		}
	}
}

func TestPlanAssignsEverySessionAndHoldsTheWeights(t *testing.T) {
	plan, err := ParsePlan("local-64k:7,idb-64k:3")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	counts := map[string]int{}
	for id := int64(1); id <= 1000; id++ {
		dataset := plan.For(id)
		if dataset.Label == "" {
			t.Fatalf("session %d got no dataset", id)
		}
		counts[dataset.Label]++
	}
	// 70/30 over a thousand sessions; the hash is not a perfect divisor, so allow
	// a few percent.
	if got := counts["local-64k"]; got < 640 || got > 760 {
		t.Errorf("%d sessions on local-64k, want about 700", got)
	}
	if got := counts["idb-64k"]; got < 240 || got > 360 {
		t.Errorf("%d sessions on idb-64k, want about 300", got)
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	first, err := ParsePlan("local-64k:1,idb-64k:1,cache-64k:1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	second, err := ParsePlan("local-64k:1,idb-64k:1,cache-64k:1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for id := int64(1); id <= 500; id++ {
		if first.For(id).Label != second.For(id).Label {
			t.Fatalf("session %d landed on different datasets in two plans", id)
		}
	}
}

func TestEmptyPlanSeedsNothing(t *testing.T) {
	seed := Seed{}
	if got := seed.Dataset(7); got != "none" {
		t.Errorf("empty plan reports dataset %q, want none", got)
	}
	if err := seed.Seed(nil, nil, 7); err != nil {
		t.Errorf("an empty plan returned %v, want nil", err)
	}
}

func TestSeedScriptWritesEachSurface(t *testing.T) {
	cases := map[string]struct {
		dataset Dataset
		want    []string
	}{
		"local": {Dataset{Label: "local-4k", Kind: KindLocal, Bytes: 4096, Count: 4},
			[]string{"localStorage.setItem", "var bytes=0, count=4;"}},
		"session": {Dataset{Label: "session-4k", Kind: KindSession, Bytes: 4096, Count: 4},
			[]string{"sessionStorage.setItem"}},
		"cookies": {Dataset{Label: "cookies-3", Kind: KindCookies, Count: 3},
			[]string{"document.cookie=", "count=3"}},
		"idb": {Dataset{Label: "idb-4k", Kind: KindIndexedDB, Bytes: 4096, Count: 8},
			[]string{"indexedDB.open('seed-db',1)", "store.put(", "objectStore('items'"}},
		"cache": {Dataset{Label: "cache-4k", Kind: KindCache, Bytes: 4096, Count: 8},
			[]string{"caches.open('seed-cache')", "cache.put(new Request("}},
	}
	for name, tc := range cases {
		script := seedScript(tc.dataset, 7)
		for _, want := range tc.want {
			if !strings.Contains(script, want) {
				t.Errorf("%s script is missing %q", name, want)
			}
		}
		if !strings.Contains(script, "letters.charAt") {
			t.Errorf("%s script does not fill its payload", name)
		}
	}
}

func TestSeedScriptIsDeterministicPerSession(t *testing.T) {
	dataset := Dataset{Label: "local-1k", Kind: KindLocal, Bytes: 1024, Count: 4}
	if a, b := seedScript(dataset, 11), seedScript(dataset, 11); a != b {
		t.Error("the same session produced two different seed scripts")
	}
	if a, b := seedScript(dataset, 11), seedScript(dataset, 12); a == b {
		t.Error("two sessions produced the same seed script; the state would not vary")
	}
}

func TestDatasetLedgerCountsPerLabel(t *testing.T) {
	var ledger datasetLedger
	ledger.record("local-64k", 100)
	ledger.record("local-64k", 200)
	ledger.record("idb-64k", 50)
	ledger.record("", 999) // no label, no entry

	entries := ledger.list()
	if len(entries) != 2 {
		t.Fatalf("%d entries, want 2: %+v", len(entries), entries)
	}
	if entries[0].Label != "idb-64k" || entries[1].Label != "local-64k" {
		t.Errorf("entries are not in label order: %+v", entries)
	}
	if entries[1].Sessions != 2 || entries[1].Bytes != 300 {
		t.Errorf("local-64k recorded %+v, want 2 sessions and 300 bytes", entries[1])
	}
}
