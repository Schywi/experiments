package queue

import (
	"time"
)

// Percentile returns the p-th percentile of values, nearest-rank on the lower
// side. It is report.percentile's rule, repeated here so a queue result and a
// run report never disagree about what p95 means.
func Percentile(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]int64, len(values))
	copy(sorted, values)
	sortInt64(sorted)
	index := int(float64(len(sorted)-1) * p / 100)
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func sortInt64(values []int64) {
	// Insertion sort is enough for the sizes a report summarises and keeps this
	// file dependency-free; the simulator's own sort uses the standard library.
	for i := 1; i < len(values); i++ {
		v := values[i]
		j := i - 1
		for j >= 0 && values[j] > v {
			values[j+1] = values[j]
			j--
		}
		values[j+1] = v
	}
}

// Summary is one class's wait distribution.
type Summary struct {
	Class   string `json:"class"`
	Count   int    `json:"count"`
	P50NS   int64  `json:"p50_ns"`
	P95NS   int64  `json:"p95_ns"`
	P99NS   int64  `json:"p99_ns"`
	MaxNS   int64  `json:"max_ns"`
	MeanNS  int64  `json:"mean_ns"`
	Starved int    `json:"starved"`
}

// Summarise reduces a Result to one row per class, in rank order.
func Summarise(res Result) []Summary {
	out := make([]Summary, 0, len(res.Waits))
	for _, class := range classOrder() {
		waits := res.Waits[class]
		if len(waits) == 0 {
			continue
		}
		total := int64(0)
		for _, wait := range waits {
			total += wait
		}
		out = append(out, Summary{
			Class:   string(class),
			Count:   len(waits),
			P50NS:   Percentile(waits, 50),
			P95NS:   Percentile(waits, 95),
			P99NS:   Percentile(waits, 99),
			MaxNS:   Percentile(waits, 100),
			MeanNS:  total / int64(len(waits)),
			Starved: res.Starved[class],
		})
	}
	return out
}

// TotalWaitNS is every class's wait added up: the single number a policy moves by
// reshuffling, which stays close to constant while the fleet is saturated.
func TotalWaitNS(res Result) int64 {
	total := int64(0)
	for _, waits := range res.Waits {
		for _, wait := range waits {
			total += wait
		}
	}
	return total
}

// MS renders a nanosecond count as milliseconds, the unit the run reports use.
func MS(ns int64) time.Duration { return time.Duration(ns) }
