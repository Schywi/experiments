package test

import (
	"strings"
	"testing"
)

func TestRiotURLsAreDistinctAndRotate(t *testing.T) {
	urls := riotURLs(len(riotChampions) + 2)
	if len(urls) != len(riotChampions)+2 {
		t.Fatalf("built %d urls for %d sessions", len(urls), len(riotChampions)+2)
	}
	if urls[0] != riotHeavy {
		t.Fatalf("session 1 is %q, want the heavy champion page", urls[0])
	}
	seen := map[string]bool{}
	for i, url := range urls {
		if url == "" {
			t.Fatalf("session %d has an empty url", i+1)
		}
		if seen[url] {
			t.Fatalf("session %d repeats %s; a screenshot could not tell them apart", i+1, url)
		}
		seen[url] = true
	}
	// The champion rotates first ...
	if !strings.Contains(urls[1], "Yasuo_0") || !strings.Contains(urls[2], "LeBlanc_0") {
		t.Fatalf("champion did not rotate: %q then %q", urls[1], urls[2])
	}
	// ... and the skin number steps once the list wraps.
	last := urls[len(urls)-1]
	if !strings.HasSuffix(last, "_1.jpg") {
		t.Fatalf("session %d is %q, want skin 1 after a full champion pass", len(urls), last)
	}
}

func TestRiotHeavySessionsAreConfigurable(t *testing.T) {
	// Three heavy sessions out of ten: the first three load the champion page, the
	// rest splashes. This is the spike-convergence knob.
	urls := riotURLsWithHeavy(10, 3)
	if len(urls) != 10 {
		t.Fatalf("built %d urls", len(urls))
	}
	for i := 0; i < 3; i++ {
		if urls[i] != riotHeavy {
			t.Fatalf("session %d is %q, want the heavy page", i+1, urls[i])
		}
	}
	if urls[3] == riotHeavy {
		t.Fatalf("session 4 is still heavy")
	}
	if urls[3] == urls[4] {
		t.Fatalf("splash URLs repeat")
	}
	// Zero heavy sessions means every session gets a splash.
	all := riotURLsWithHeavy(4, 0)
	for i, url := range all {
		if url == riotHeavy {
			t.Fatalf("session %d is heavy with heavy=0", i+1)
		}
	}
}

func TestRiotURLsOfNothing(t *testing.T) {
	if got := riotURLs(0); got != nil {
		t.Fatalf("riotURLs(0) = %v, want nil", got)
	}
}
