package queue

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
)

func TestClassByHoldUsesTheMixEdges(t *testing.T) {
	classOf := ClassByHold(30*sec, 120*sec)
	cases := []struct {
		hold int64
		want model.Class
	}{
		{30 * sec, model.ClassNormal},
		{30*sec + 1, model.ClassBatch},
		{120 * sec, model.ClassBatch},
		{5 * min_, model.ClassBulk},
		{0, model.ClassNormal},
	}
	for _, c := range cases {
		if got := classOf("", c.hold); got != c.want {
			t.Fatalf("hold %v classed %s, want %s", time.Duration(c.hold), got, c.want)
		}
	}
}

func TestTraceFromPlanKeepsArrivalsAndHolds(t *testing.T) {
	plan := schedule.Plan{Sessions: []schedule.Planned{
		{ID: 2, Bucket: "5m", Hold: 5 * time.Minute, Offset: 2 * time.Second},
		{ID: 1, Bucket: "30s", Hold: 30 * time.Second, Offset: time.Second},
	}}
	trace := TraceFromPlan(plan, ClassByHold(30*sec, 120*sec))
	if len(trace) != 2 {
		t.Fatalf("trace has %d requests", len(trace))
	}
	// Sorted by arrival: id 1 first.
	if trace[0].ID != 1 || trace[0].Hold != 30*sec || trace[0].Class != model.ClassNormal {
		t.Fatalf("first request %+v", trace[0])
	}
	if trace[1].Class != model.ClassBulk {
		t.Fatalf("5m session classed %s", trace[1].Class)
	}
	if got := Span(trace); got != sec {
		t.Fatalf("span %v, want 1s", time.Duration(got))
	}
	if got := MeanHold(trace); got != 165*time.Second {
		t.Fatalf("mean hold %v, want 165s", got)
	}
}

func TestTraceFromRowsSkipsSessionsWithoutAHold(t *testing.T) {
	rows := []model.Session{
		{ID: 1, ArrivalNS: 100, HoldStartNS: 200, HoldEndNS: 300},
		{ID: 2, ArrivalNS: 150, HoldStartNS: 0, HoldEndNS: 0},
		{ID: 3, ArrivalNS: 0, HoldStartNS: 200, HoldEndNS: 300},
	}
	trace, skipped := TraceFromRows(rows, model.ClassNormal)
	if len(trace) != 1 || skipped != 2 {
		t.Fatalf("trace %+v skipped %d, want one request and two skipped", trace, skipped)
	}
	if trace[0].Arrival != 0 || trace[0].Hold != 100 {
		t.Fatalf("request %+v", trace[0])
	}
}

func TestSplitWaitDerivesTheThreeGates(t *testing.T) {
	s := model.Session{
		ArrivalNS:       1_000,
		AdmissionWaitNS: 100,
		ClaimCreatedNS:  1_500, // 400 ns between admission and the claim: the boot permit
		ReadyNS:         2_000, // 500 ns in the pool queue
	}
	admit, boot, slot, total, ok := SplitWait(s)
	if !ok {
		t.Fatalf("a well-formed row was rejected")
	}
	if admit != 100 || boot != 400 || slot != 500 || total != 1_000 {
		t.Fatalf("split admit %d boot %d slot %d total %d", admit, boot, slot, total)
	}
}

func TestSplitWaitRejectsRowsThatDoNotTelescope(t *testing.T) {
	// Admission alone claims more time than the whole session took.
	s := model.Session{ArrivalNS: 1_000, AdmissionWaitNS: 900, ClaimCreatedNS: 1_100, ReadyNS: 1_500}
	if _, _, _, _, ok := SplitWait(s); ok {
		t.Fatalf("an impossible row was accepted")
	}
	// No lease at all.
	if _, _, _, _, ok := SplitWait(model.Session{ArrivalNS: 1_000}); ok {
		t.Fatalf("a session that never got a lease was accepted")
	}
}

func TestSplitsForReportsCountAndPercentiles(t *testing.T) {
	// Zero means "not observed" in these rows, so a replayable session needs a
	// real arrival even in a unit test.
	rows := []model.Session{
		{ArrivalNS: 100, AdmissionWaitNS: 10, ClaimCreatedNS: 120, ReadyNS: 130},
		{ArrivalNS: 100, AdmissionWaitNS: 20, ClaimCreatedNS: 140, ReadyNS: 200},
		{ArrivalNS: 0}, // dropped
	}
	splits := SplitsFor(rows)
	if splits.Total.Count != 2 {
		t.Fatalf("count %d, want 2", splits.Total.Count)
	}
	if splits.Slot.P50NS != 10 || splits.Slot.MaxNS != 60 {
		t.Fatalf("slot percentiles %+v", splits.Slot)
	}
	if splits.Boot.Count != 2 {
		t.Fatalf("boot count %d", splits.Boot.Count)
	}
}

func TestSaveAndLoadTraceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.json")
	trace := threeJobs()
	if err := SaveTrace(path, trace); err != nil {
		t.Fatalf("save: %v", err)
	}
	back, err := LoadTrace(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(back) != len(trace) {
		t.Fatalf("loaded %d requests, want %d", len(back), len(trace))
	}
	if back[0].ID != trace[0].ID || back[0].Hold != trace[0].Hold || back[0].Class != trace[0].Class {
		t.Fatalf("first request changed: %+v vs %+v", back[0], trace[0])
	}
}

func TestClassByHoldTwoLeavesBatchEmpty(t *testing.T) {
	// The chosen arm is normal/bulk: batch must be unreachable rather than
	// reachable-by-accident, so no hold maps to it.
	classOf := ClassByHoldTwo(30 * sec)
	for _, hold := range []int64{0, sec, 30 * sec, 31 * sec, 5 * min_, 300 * min_} {
		if got := classOf("", hold); got == model.ClassBatch {
			t.Fatalf("hold %d classed batch under the two-class partition", hold)
		}
	}
	if got := classOf("", 30*sec); got != model.ClassNormal {
		t.Fatalf("30s classed %s, want normal", got)
	}
	if got := classOf("", 31*sec); got != model.ClassBulk {
		t.Fatalf("31s classed %s, want bulk", got)
	}
}
