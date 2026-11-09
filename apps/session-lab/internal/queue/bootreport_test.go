package queue

import "testing"

// Timestamps here start at a non-zero base because a zero means "not observed" in
// the rows this reads, and Duration treats it as a boot that never happened.
const base = int64(1_000)

func TestAnalyseBootsBucketsByMeasuredConcurrency(t *testing.T) {
	boots := []Boot{
		{ID: "a", Created: base, Ready: base + 100},
		{ID: "b", Created: base + 10, Ready: base + 200}, // overlaps a
		{ID: "c", Created: base + 300, Ready: base + 340},
	}
	sessions := []SessionSpan{
		{Instance: "a", Arrival: base, Ready: base + 100},
		{Instance: "b", Arrival: base + 10, Ready: base + 200},
		{Instance: "c", Arrival: base + 300, Ready: base + 340},
	}
	report := AnalyseBoots(boots, sessions)
	if report.PeakConcurrency != 2 {
		t.Fatalf("peak concurrency %d, want 2", report.PeakConcurrency)
	}
	if len(report.Buckets) != 2 {
		t.Fatalf("buckets %+v, want two", report.Buckets)
	}
	// Buckets are ordered by concurrency, so look them up rather than index them.
	byK := map[int]BootBucket{}
	for _, bucket := range report.Buckets {
		byK[bucket.Concurrency] = bucket
	}
	if got := byK[2]; got.Boots != 2 {
		t.Fatalf("k=2 bucket %+v, want two boots", got)
	}
	if got := byK[1]; got.P50NS != 40 {
		t.Fatalf("k=1 bucket %+v, want a 40 ns boot", got)
	}
	if report.Unused != 0 {
		t.Fatalf("unused %d, want 0", report.Unused)
	}
}

// A boot that becomes ready while sessions wait and binds none is a defect; one
// that becomes ready with nobody waiting is slack, and the two must not be summed.
func TestAnalyseBootsSeparatesUnboundFromSlack(t *testing.T) {
	boots := []Boot{
		{ID: "used", Created: base, Ready: base + 50},
		{ID: "waited", Created: base, Ready: base + 60},
		{ID: "slack", Created: base + 500, Ready: base + 520},
	}
	sessions := []SessionSpan{
		{Instance: "used", Arrival: base, Ready: base + 50},
		// Arrived before "waited" was ready, and bound before "slack" was ready:
		// one boot landed while it waited, the other landed with an empty queue.
		{Instance: "other", Arrival: base + 10, Ready: base + 500},
	}
	report := AnalyseBoots(boots, sessions)
	if len(report.Buckets) != 2 {
		t.Fatalf("buckets %+v, want two", report.Buckets)
	}
	var waited, slack int
	for _, bucket := range report.Buckets {
		waited += bucket.IdleWaited
		slack += bucket.IdleUnused
		if bucket.Served < 0 || bucket.Served > bucket.Boots {
			t.Fatalf("impossible served count %+v", bucket)
		}
	}
	if waited != 1 {
		t.Fatalf("unbound-ready %d, want 1", waited)
	}
	if slack != 1 {
		t.Fatalf("slack %d, want 1", slack)
	}
	if report.Unused != 2 {
		t.Fatalf("unused %d, want 2", report.Unused)
	}
}
