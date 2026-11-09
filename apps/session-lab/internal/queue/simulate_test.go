package queue

import (
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// ms renders a nanosecond count as a duration so a failure message reads in the
// unit the run reports use.
func ms(ns int64) time.Duration { return time.Duration(ns) }

const (
	sec  = int64(1_000_000_000)
	min_ = 60 * sec
)

// threeJobs is the worked example from the theory note: 300 s, 120 s and 30 s
// arriving together at one slot.
func threeJobs() []Request {
	return []Request{
		{ID: 1, Class: model.ClassNormal, Arrival: 0, Hold: 300 * sec},
		{ID: 2, Class: model.ClassNormal, Arrival: 0, Hold: 120 * sec},
		{ID: 3, Class: model.ClassNormal, Arrival: 0, Hold: 30 * sec},
	}
}

func TestSimulateFIFOMatchesTheHandArithmetic(t *testing.T) {
	res := Simulate(threeJobs(), Config{Slots: 1, Policy: NewFIFO()})
	// Arrival order 300, 120, 30 at one slot: starts 0, 300, 420.
	// Mean wait 240 s, mean response 360 s.
	if got, want := res.Waits[model.ClassNormal], []int64{0, 300 * sec, 420 * sec}; !equalInt64(got, want) {
		t.Fatalf("waits %v, want %v", got, want)
	}
	if got := res.Makespan; got != 450*sec {
		t.Fatalf("makespan %v, want 450s", ms(got))
	}
	if res.Started != 3 {
		t.Fatalf("started %d, want 3", res.Started)
	}
}

func TestSimulateShortestFirstCutsMeanResponse(t *testing.T) {
	fifo := Simulate(threeJobs(), Config{Slots: 1, Policy: NewFIFO()})
	sjf := Simulate(threeJobs(), Config{Slots: 1, Policy: NewOracle(true)})
	// Waits for 30, 120, 300 at one slot are 0, 30, 150 s: mean wait 60 s.
	if got, want := sjf.Waits[model.ClassNormal], []int64{0, 30 * sec, 150 * sec}; !equalInt64(got, want) {
		t.Fatalf("oracle waits %v, want %v", got, want)
	}
	// The makespan is the same work either way; only who pays it changes.
	if fifo.Makespan != sjf.Makespan {
		t.Fatalf("makespan moved with the order: %v vs %v", ms(fifo.Makespan), ms(sjf.Makespan))
	}
	if TotalWaitNS(sjf) >= TotalWaitNS(fifo) {
		t.Fatalf("shortest-first did not reduce total wait: %v vs %v", ms(TotalWaitNS(sjf)), ms(TotalWaitNS(fifo)))
	}
}

func TestSimulatePriorityServesTheNormalClassFirst(t *testing.T) {
	// Mixed classes in arrival order bulk, normal, bulk: priority serves the
	// normal one first and the two bulk ones in arrival order after it.
	trace := []Request{
		{ID: 1, Class: model.ClassBulk, Arrival: 0, Hold: 10 * sec},
		{ID: 2, Class: model.ClassNormal, Arrival: sec, Hold: 10 * sec},
		{ID: 3, Class: model.ClassBulk, Arrival: 2 * sec, Hold: 10 * sec},
	}
	res := Simulate(trace, Config{Slots: 1, Policy: NewPriority()})
	if got, want := res.Waits[model.ClassNormal], []int64{9 * sec}; !equalInt64(got, want) {
		t.Fatalf("normal wait %v, want %v", got, want)
	}
	// Bulk 1 starts immediately; bulk 3 waits behind the normal session, which
	// holds its lease from 10 s to 20 s.
	if got, want := res.Waits[model.ClassBulk], []int64{0, 18 * sec}; !equalInt64(got, want) {
		t.Fatalf("bulk waits %v, want %v", got, want)
	}
}

func TestSimulateElasticFleetPaysBootBeforeAnySlotExists(t *testing.T) {
	trace := []Request{
		{ID: 1, Class: model.ClassNormal, Arrival: 0, Hold: 100 * sec},
		{ID: 2, Class: model.ClassNormal, Arrival: 0, Hold: 100 * sec},
		{ID: 3, Class: model.ClassNormal, Arrival: 0, Hold: 100 * sec},
	}
	// One lease per pod, one pod creation at a time, 40 s to boot. Nothing can
	// start before the first pod is usable and each session needs a pod of its
	// own, so the starts are exactly one boot apart.
	res := Simulate(trace, Config{
		Policy: NewFIFO(), SlotsPerPod: 1, MaxPods: 3, Boot: 40 * sec, Provisioning: 1,
	})
	if res.Started != 3 {
		t.Fatalf("started %d, want 3", res.Started)
	}
	if got, want := res.Waits[model.ClassNormal], []int64{40 * sec, 80 * sec, 120 * sec}; !equalInt64(got, want) {
		t.Fatalf("waits %v, want %v", ms(got[0]), want)
	}
	if res.PeakPods != 3 {
		t.Fatalf("peak pods %d, want 3", res.PeakPods)
	}
}

func TestSimulateAFullFleetSerialisesRatherThanInventingCapacity(t *testing.T) {
	trace := []Request{
		{ID: 1, Class: model.ClassNormal, Arrival: 0, Hold: 10 * sec},
		{ID: 2, Class: model.ClassNormal, Arrival: 0, Hold: 10 * sec},
	}
	res := Simulate(trace, Config{Policy: NewFIFO(), SlotsPerPod: 1, MaxPods: 1, Boot: sec, Provisioning: 1})
	if res.PeakPods != 1 {
		t.Fatalf("peak pods %d, want 1", res.PeakPods)
	}
	if got, want := res.Waits[model.ClassNormal], []int64{sec, 11 * sec}; !equalInt64(got, want) {
		t.Fatalf("waits %v, want %v", ms(got[0]), want)
	}
}

func TestSimulateChargesDecisionLatency(t *testing.T) {
	fast := Simulate(threeJobs(), Config{Slots: 1, Policy: NewFIFO()})
	slow := Simulate(threeJobs(), Config{Slots: 1, Policy: NewFIFO(), DecisionLatency: 100_000})
	if slow.DecisionNS <= 0 {
		t.Fatalf("no decision cost was recorded")
	}
	if slow.Decisions != fast.Decisions {
		t.Fatalf("latency changed the number of decisions: %d vs %d", slow.Decisions, fast.Decisions)
	}
	if slow.Makespan <= fast.Makespan {
		t.Fatalf("a priced decision did not delay the schedule")
	}
}

func TestSimulateIsDeterministic(t *testing.T) {
	a := Simulate(threeJobs(), Config{Slots: 1, Policy: NewPriority()})
	b := Simulate(threeJobs(), Config{Slots: 1, Policy: NewPriority()})
	if !equalInt64(a.Waits[model.ClassNormal], b.Waits[model.ClassNormal]) {
		t.Fatalf("two runs of the same trace disagreed")
	}
	if a.Makespan != b.Makespan || a.Decisions != b.Decisions {
		t.Fatalf("two runs of the same trace disagreed on the schedule")
	}
}

func TestSimulatePriorityPushesTheBulkClassBehind(t *testing.T) {
	trace := []Request{{ID: 100, Class: model.ClassBulk, Arrival: 0, Hold: sec}}
	for i := 0; i < 8; i++ {
		trace = append(trace, Request{ID: int64(i), Class: model.ClassNormal, Arrival: int64(i) * sec, Hold: 10 * sec})
	}
	fifo := Simulate(trace, Config{Slots: 1, Policy: NewFIFO()})
	prio := Simulate(trace, Config{Slots: 1, Policy: NewPriority()})
	if prio.Waits[model.ClassBulk][0] <= fifo.Waits[model.ClassBulk][0] {
		t.Fatalf("priority did not push the bulk session behind the normal ones: %v vs %v",
			ms(prio.Waits[model.ClassBulk][0]), ms(fifo.Waits[model.ClassBulk][0]))
	}
	if prio.Waits[model.ClassNormal][0] != 0 {
		t.Fatalf("normal session waited %v under priority", ms(prio.Waits[model.ClassNormal][0]))
	}
}

func TestStarvedCountsWaitsBeyondFiveTimesTheClassMean(t *testing.T) {
	// Nine immediate waits and one of 1,000 s: the mean is 100 s, so the
	// threshold is 500 s and only the outlier is starved. The definition needs
	// roughly six samples before any single wait can exceed five times the mean,
	// which is worth knowing when a class is small.
	res := Result{Waits: map[model.Class][]int64{
		model.ClassBulk: {0, 0, 0, 0, 0, 0, 0, 0, 0, 1000 * sec},
	}}
	markStarved(&res)
	if res.Starved[model.ClassBulk] != 1 {
		t.Fatalf("starved %d, want 1", res.Starved[model.ClassBulk])
	}
}

func TestUtilisationCannotExceedOne(t *testing.T) {
	trace := []Request{
		{ID: 1, Class: model.ClassNormal, Arrival: 0, Hold: 10 * sec},
		{ID: 2, Class: model.ClassNormal, Arrival: sec, Hold: 10 * sec},
	}
	res := Simulate(trace, Config{Slots: 1, Policy: NewFIFO()})
	if res.Utilisation <= 0 || res.Utilisation > 1.0001 {
		t.Fatalf("utilisation %.3f is outside (0,1]", res.Utilisation)
	}
	if res.SlotNS <= 0 {
		t.Fatalf("no slot-time was accumulated")
	}
}

func TestSummariseReportsPercentilesPerClass(t *testing.T) {
	res := Simulate(threeJobs(), Config{Slots: 1, Policy: NewFIFO()})
	rows := Summarise(res)
	if len(rows) != 1 {
		t.Fatalf("rows %v", rows)
	}
	row := rows[0]
	if row.Class != "normal" || row.Count != 3 {
		t.Fatalf("row %+v", row)
	}
	if row.P50NS != 300*sec || row.MaxNS != 420*sec {
		t.Fatalf("percentiles %+v", row)
	}
}

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
