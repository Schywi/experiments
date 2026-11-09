package queue

import (
	"fmt"
	"testing"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// deepBacklog builds depth waiting requests of one class, which is what
// betterWaiting scans on every Acquire today.
func deepBacklog(depth int) []*Request {
	out := make([]*Request, 0, depth)
	for i := 0; i < depth; i++ {
		out = append(out, &Request{
			ID:      int64(i),
			Class:   model.ClassBulk,
			Arrival: int64(i) * 1_000,
			Hold:    30 * sec,
		})
	}
	return out
}

// BenchmarkDecide measures what one Offer plus one Peek costs at the depths a run
// reaches: the number HANDOFF.md §4 asks for, so "the scan is O(n)" becomes a
// measurement rather than a claim.
func BenchmarkDecide(b *testing.B) {
	for _, depth := range []int{1, 100, 1_000, 10_000} {
		for _, kind := range Kinds {
			b.Run(fmt.Sprintf("%s/depth=%d", kind, depth), func(b *testing.B) {
				p, err := New(kind, Options{AgingNS: 30 * sec, DemoteAfterNS: 30 * sec})
				if err != nil {
					b.Fatalf("%s: %v", kind, err)
				}
				backlog := deepBacklog(depth)
				for _, r := range backlog {
					p.Offer(r, 0)
				}
				now := int64(depth) * 1_000
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r := &Request{ID: int64(depth + i), Class: model.ClassNormal, Arrival: now, Hold: 30 * sec}
					p.Offer(r, now)
					if got := p.Peek(now); got != nil {
						// Take keeps the depth steady, so each iteration
						// measures the same queue rather than one that grows.
						p.Take(got)
					}
					now += 1_000
				}
			})
		}
	}
}

// BenchmarkSimulate is the whole comparison loop for the recorded shape, so the
// cost of running every policy over one trace is a number rather than a guess.
func BenchmarkSimulate(b *testing.B) {
	trace := make([]Request, 0, 3_000)
	for i := 0; i < 3_000; i++ {
		hold := int64(30 * sec)
		class := model.ClassNormal
		switch {
		case i%10 >= 7:
			hold, class = 120*sec, model.ClassBatch
		case i%10 >= 5:
			hold, class = 300*sec, model.ClassBulk
		}
		trace = append(trace, Request{
			ID:      int64(i),
			Class:   class,
			Arrival: int64(i) * (86 * sec / 3_000),
			Hold:    hold,
		})
	}
	for _, kind := range Kinds {
		b.Run(kind, func(b *testing.B) {
			p, err := New(kind, Options{AgingNS: 30 * sec, DemoteAfterNS: 30 * sec})
			if err != nil {
				b.Fatalf("%s: %v", kind, err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				Simulate(trace, Config{
					Policy: p, SlotsPerPod: 8, MaxPods: 38, Boot: 40 * sec, Provisioning: 1,
				})
			}
		})
	}
}
