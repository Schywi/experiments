package queue

import (
	"sort"
	"time"
)

// Boot is one instance's cold path, as the store recorded it.
type Boot struct {
	ID      string
	Created int64
	Ready   int64
}

// Duration is how long that boot took, zero when it never became ready.
func (b Boot) Duration() int64 {
	if b.Ready <= 0 || b.Created <= 0 {
		return 0
	}
	return b.Ready - b.Created
}

// SessionSpan is the part of a session's life the boot analysis needs: when it
// arrived, when its lease became usable, and how long it held it.
type SessionSpan struct {
	Instance  string
	Arrival   int64
	Ready     int64
	HoldStart int64
	HoldEnd   int64
}

// BootBucket is one row of the report: every boot that overlapped this many others.
type BootBucket struct {
	// Concurrency is how many boots were in flight at the same time, counting
	// this one. It is measured from the rows, not from the flag the run was
	// started with, which is the whole point: an experiment can claim k=4 while
	// the pool serialises and every boot sees concurrency 1.
	Concurrency int   `json:"concurrency"`
	Boots       int   `json:"boots"`
	P50NS       int64 `json:"p50_ns"`
	P95NS       int64 `json:"p95_ns"`
	MaxNS       int64 `json:"max_ns"`
	// PerMinute is ready browsers per minute inside this bucket.
	PerMinute float64 `json:"per_minute"`
	// Served counts instances in the bucket that served at least one session.
	Served int `json:"served"`
	// IdleUnused counts instances that served nobody while nothing was waiting
	// when they became ready: the boots that were genuinely unnecessary.
	IdleUnused int `json:"idle_unused"`
	// IdleWaited counts instances that served nobody although sessions were
	// waiting when they became ready. Those are a different defect: a ready
	// browser nobody could bind.
	IdleWaited int `json:"idle_waited"`
}

// BootReport is what one run's rows say about boot throughput.
type BootReport struct {
	Boots   int          `json:"boots"`
	Buckets []BootBucket `json:"buckets"`
	// PeakConcurrency is the largest number of boots in flight at once, which is
	// what the run actually achieved whatever it was configured for.
	PeakConcurrency int `json:"peak_concurrency"`
	// Unused is every instance that served no session, whatever the reason.
	Unused int `json:"unused"`
}

// AnalyseBoots groups boots by measured concurrency and counts what each one was
// worth. Sessions are matched to instances by the instance id the runner wrote on
// the session row.
func AnalyseBoots(boots []Boot, sessions []SessionSpan) BootReport {
	report := BootReport{Boots: len(boots)}
	if len(boots) == 0 {
		return report
	}

	servedBy := map[string]int{}
	for _, s := range sessions {
		if s.Instance != "" {
			servedBy[s.Instance]++
		}
	}

	// Waiting at an instant is the sessions that had arrived and had no usable
	// lease yet. Comparing against the whole run is what makes "was this boot
	// needed" answerable.
	waitingAt := func(at int64) int {
		waiting := 0
		for _, s := range sessions {
			if s.Arrival <= at && (s.Ready == 0 || s.Ready > at) {
				waiting++
			}
		}
		return waiting
	}

	byConcurrency := map[int][]Boot{}
	for _, b := range boots {
		if b.Duration() <= 0 {
			continue
		}
		concurrency := 1
		for _, other := range boots {
			if other.ID == b.ID || other.Duration() <= 0 {
				continue
			}
			// Half-open overlap: a boot that finished exactly when another began
			// was not in flight at the same time.
			if other.Created < b.Ready && b.Created < other.Ready {
				concurrency++
			}
		}
		byConcurrency[concurrency] = append(byConcurrency[concurrency], b)
		if concurrency > report.PeakConcurrency {
			report.PeakConcurrency = concurrency
		}
	}

	for concurrency, group := range byConcurrency {
		durations := make([]int64, 0, len(group))
		bucket := BootBucket{Concurrency: concurrency, Boots: len(group)}
		for _, b := range group {
			durations = append(durations, b.Duration())
			// Two kinds of unused browser, and they mean different things: one that
			// became ready while sessions were waiting but bound none (a ready
			// browser nobody could use, a defect), and one that became ready with
			// nobody waiting. The second is not automatically waste -- it may serve
			// the next arrival -- so it is reported as idle rather than as
			// worthless, and the caller reads it beside the served count.
			switch {
			case servedBy[b.ID] > 0:
				bucket.Served++
			case waitingAt(b.Ready) > 0:
				bucket.IdleWaited++
			default:
				bucket.IdleUnused++
			}
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		bucket.P50NS = Percentile(durations, 50)
		bucket.P95NS = Percentile(durations, 95)
		bucket.MaxNS = Percentile(durations, 100)
		if span := readySpan(group); span > 0 {
			bucket.PerMinute = float64(len(group)) / (float64(span) / float64(time.Minute))
		}
		report.Buckets = append(report.Buckets, bucket)
	}
	report.Unused = report.Boots - countServed(boots, servedBy)
	sort.Slice(report.Buckets, func(i, j int) bool {
		return report.Buckets[i].Concurrency < report.Buckets[j].Concurrency
	})
	return report
}

func readySpan(boots []Boot) int64 {
	first, last := int64(0), int64(0)
	for _, b := range boots {
		if first == 0 || b.Ready < first {
			first = b.Ready
		}
		if b.Ready > last {
			last = b.Ready
		}
	}
	return last - first
}

func countServed(boots []Boot, servedBy map[string]int) int {
	served := 0
	for _, b := range boots {
		if servedBy[b.ID] > 0 {
			served++
		}
	}
	return served
}
