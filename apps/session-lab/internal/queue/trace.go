package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
)

// ClassOf assigns a workload class to a session from what is known when it
// arrives. That is the whole input a deployable size-based policy gets.
type ClassOf func(bucket string, hold int64) model.Class

// ClassByHold classes a session by the browser time it says it will need: at or
// below fastNS is normal, up to normalNS is batch, beyond is bulk. The defaults
// are the recorded mix's own edges (30 s, 2 min, 5 min).
func ClassByHold(fastNS, normalNS int64) ClassOf {
	return func(_ string, hold int64) model.Class {
		switch {
		case hold <= 0:
			return model.ClassNormal
		case hold <= fastNS:
			return model.ClassNormal
		case hold <= normalNS:
			return model.ClassBatch
		default:
			return model.ClassBulk
		}
	}
}

// ClassByHoldTwo is the two-class variant: normal at or below normalNS, bulk
// above it. The queue study's owner chose a normal/bulk partition, so this is the
// classifier the live arms use; batch is left empty on purpose rather than made
// unreachable by setting two thresholds equal.
func ClassByHoldTwo(normalNS int64) ClassOf {
	return func(_ string, hold int64) model.Class {
		if hold <= 0 || hold <= normalNS {
			return model.ClassNormal
		}
		return model.ClassBulk
	}
}

// FixedClass gives every session one class, which is what a single-class run is.
func FixedClass(class model.Class) ClassOf {
	return func(string, int64) model.Class { return class }
}

// TraceFromPlan turns the workload the runner would generate into a trace. The
// holds are the plan's own, so a policy that reads Hold is an oracle on this
// trace and says nothing about a live run.
func TraceFromPlan(plan schedule.Plan, classOf ClassOf) []Request {
	trace := make([]Request, 0, len(plan.Sessions))
	for _, planned := range plan.Sessions {
		hold := int64(planned.Hold)
		trace = append(trace, Request{
			ID:      planned.ID,
			Class:   classOf(planned.Bucket, hold),
			Arrival: int64(planned.Offset),
			Hold:    hold,
			// A generated workload declares its holds, so the plan's own hold is
			// the prediction the platform would carry at arrival. With no noise
			// added to it, oracle-est and oracle-sjf are the same arm; --estimate
			// -noise in queue-lab is what separates them.
			EstHold: hold,
			Bucket:  planned.Bucket,
		})
	}
	sort.SliceStable(trace, func(i, j int) bool {
		if trace[i].Arrival != trace[j].Arrival {
			return trace[i].Arrival < trace[j].Arrival
		}
		return trace[i].ID < trace[j].ID
	})
	return trace
}

// TraceFromRows rebuilds the trace a recorded run actually lived: arrival times
// as they were, and the hold each session held for. Sessions with no arrival or
// no hold observation cannot be replayed and are counted as skipped.
//
// The class comes from the run, not from the row: model.Session has no class
// field, because --class is a property of a run rather than of a session. A
// multi-class trace can only be built from a plan or from a JSON file.
func TraceFromRows(sessions []model.Session, class model.Class) (trace []Request, skipped int) {
	base := int64(0)
	for _, s := range sessions {
		if s.ArrivalNS > 0 && (base == 0 || s.ArrivalNS < base) {
			base = s.ArrivalNS
		}
	}
	for _, s := range sessions {
		if s.ArrivalNS <= 0 || s.HoldStartNS <= 0 || s.HoldEndNS <= s.HoldStartNS {
			skipped++
			continue
		}
		trace = append(trace, Request{
			ID:      s.ID,
			Class:   class,
			Arrival: s.ArrivalNS - base,
			Hold:    s.HoldEndNS - s.HoldStartNS,
			Bucket:  s.Bucket,
		})
	}
	sort.SliceStable(trace, func(i, j int) bool {
		if trace[i].Arrival != trace[j].Arrival {
			return trace[i].Arrival < trace[j].Arrival
		}
		return trace[i].ID < trace[j].ID
	})
	return trace, skipped
}

// Split is where a recorded run's wait went: the three gates the runner has, per
// session, derived from the timestamps the store already writes.
type Split struct {
	Count  int   `json:"count"`
	P50NS  int64 `json:"p50_ns"`
	P95NS  int64 `json:"p95_ns"`
	MaxNS  int64 `json:"max_ns"`
	MeanNS int64 `json:"mean_ns"`
}

// Splits is a run's whole wait, measured three ways.
type Splits struct {
	// Admit is arrival → admitted, recorded by the runner as AdmissionWaitNS.
	Admit Split `json:"admit"`
	// Boot is admitted → claim created, which no field records: it is the second
	// admission gate, the one --boot-concurrency bounds.
	Boot Split `json:"boot"`
	// Slot is claim created → ready, the "queue wait" the run report prints.
	Slot Split `json:"slot"`
	// Total is arrival → ready.
	Total Split `json:"total"`
}

// SplitWait derives the three waits from one session's timestamps. It returns
// false when the row cannot support the arithmetic: a session that never got a
// lease, or one whose timestamps were written by an older runner that did not
// record admission.
func SplitWait(s model.Session) (admit, boot, slot, total int64, ok bool) {
	if s.ArrivalNS <= 0 || s.ClaimCreatedNS <= 0 || s.ReadyNS <= 0 {
		return 0, 0, 0, 0, false
	}
	if s.ReadyNS < s.ClaimCreatedNS || s.ClaimCreatedNS < s.ArrivalNS {
		return 0, 0, 0, 0, false
	}
	total = s.ReadyNS - s.ArrivalNS
	slot = s.ReadyNS - s.ClaimCreatedNS
	admit = s.AdmissionWaitNS
	boot = total - admit - slot
	if boot < 0 {
		// The three timestamps do not telescope, which means the classes are
		// being attributed to the wrong gate. Report it rather than an
		// impossible negative wait.
		return 0, 0, 0, 0, false
	}
	return admit, boot, slot, total, true
}

// SplitsFor derives the split for every replayable session in a run.
func SplitsFor(sessions []model.Session) Splits {
	var admit, boot, slot, total []int64
	for _, s := range sessions {
		a, b, c, d, ok := SplitWait(s)
		if !ok {
			continue
		}
		admit = append(admit, a)
		boot = append(boot, b)
		slot = append(slot, c)
		total = append(total, d)
	}
	return Splits{
		Admit: summariseSplit(admit),
		Boot:  summariseSplit(boot),
		Slot:  summariseSplit(slot),
		Total: summariseSplit(total),
	}
}

func summariseSplit(values []int64) Split {
	if len(values) == 0 {
		return Split{}
	}
	total := int64(0)
	for _, v := range values {
		total += v
	}
	return Split{
		Count:  len(values),
		P50NS:  Percentile(values, 50),
		P95NS:  Percentile(values, 95),
		MaxNS:  Percentile(values, 100),
		MeanNS: total / int64(len(values)),
	}
}

// LoadTrace reads a trace from a JSON file, as cmd/queue-lab --simulate writes it
// so that a comparison can be reproduced without the database it came from.
func LoadTrace(path string) ([]Request, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var trace []Request
	if err := json.Unmarshal(payload, &trace); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return trace, nil
}

// SaveTrace writes a trace so the same arrivals can be replayed later.
func SaveTrace(path string, trace []Request) error {
	payload, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o644)
}

// Span is how long the trace's arrivals take to arrive, in nanoseconds.
func Span(trace []Request) int64 {
	if len(trace) == 0 {
		return 0
	}
	first, last := trace[0].Arrival, trace[0].Arrival
	for _, r := range trace {
		if r.Arrival < first {
			first = r.Arrival
		}
		if r.Arrival > last {
			last = r.Arrival
		}
	}
	return last - first
}

// MeanHold is the trace's average hold, the number Little's law multiplies the
// arrival rate by.
func MeanHold(trace []Request) time.Duration {
	if len(trace) == 0 {
		return 0
	}
	total := int64(0)
	for _, r := range trace {
		total += r.Hold
	}
	return time.Duration(total / int64(len(trace)))
}
