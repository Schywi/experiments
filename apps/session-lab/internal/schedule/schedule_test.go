package schedule

import (
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/model"
)

func TestGenerateKeepsEveryBucketCount(t *testing.T) {
	cfg := model.RunConfig{Mix: []model.Bucket{
		{Label: "30s", Hold: 30 * time.Second, Count: 5},
		{Label: "2m", Hold: 2 * time.Minute, Count: 3},
		{Label: "10m", Hold: 10 * time.Minute, Count: 2},
	}}
	plan, err := Generate(cfg, 1)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(plan.Sessions) != 10 {
		t.Fatalf("want 10 sessions, got %d", len(plan.Sessions))
	}
	counts := map[string]int{}
	for _, session := range plan.Sessions {
		counts[session.Bucket]++
	}
	for _, bucket := range cfg.Mix {
		if counts[bucket.Label] != bucket.Count {
			t.Fatalf("bucket %s: want %d sessions, got %d", bucket.Label, bucket.Count, counts[bucket.Label])
		}
	}
	// Interleaved, not blocked: the first three sessions must not all be the
	// same bucket, or the run measures one length at a time.
	if plan.Sessions[0].Bucket == plan.Sessions[1].Bucket &&
		plan.Sessions[1].Bucket == plan.Sessions[2].Bucket {
		t.Fatalf("buckets are blocked: %v", plan.Sessions[:3])
	}
}

func TestOverloadFactorMultipliesOfferedLoad(t *testing.T) {
	base := model.RunConfig{Mix: []model.Bucket{{Label: "1m", Hold: time.Minute, Count: 60}}}
	one, err := Generate(base, 1)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// 60 sessions of 60s is exactly one instance's worth of work.
	if got := one.PeakOffered(); got < 0.9 || got > 1.1 {
		t.Fatalf("factor 1 should offer ~1 concurrent session, got %.2f", got)
	}
	base.OverloadFactor = 5
	five, err := Generate(base, 1)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := five.PeakOffered(); got < 4.5 || got > 5.5 {
		t.Fatalf("factor 5 should offer ~5 concurrent sessions, got %.2f", got)
	}
	if five.Rate <= one.Rate {
		t.Fatalf("overload factor did not raise the rate: %.4f vs %.4f", five.Rate, one.Rate)
	}
}

func TestArrivalsAreOrdered(t *testing.T) {
	for _, fixed := range []bool{true, false} {
		cfg := model.RunConfig{
			Mix:            []model.Bucket{{Label: "1s", Hold: time.Second, Count: 50}},
			FixedArrival:   fixed,
			OverloadFactor: 3,
		}
		plan, err := Generate(cfg, 7)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		previous := time.Duration(-1)
		for _, session := range plan.Sessions {
			if session.Offset < previous {
				t.Fatalf("fixed=%v: arrival offsets went backwards at %d", fixed, session.ID)
			}
			previous = session.Offset
		}
	}
}

func TestNoBucketIsRefused(t *testing.T) {
	// The generator has no ceiling: a count far above any host is still generated.
	cfg := model.RunConfig{Mix: []model.Bucket{{Label: "1s", Hold: time.Second, Count: 5000}}, OverloadFactor: 50}
	plan, err := Generate(cfg, 1)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(plan.Sessions) != 5000 {
		t.Fatalf("want 5000 sessions, got %d", len(plan.Sessions))
	}
}
