package agent

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Event kinds. The vocabulary follows the one E6's controller wrote, so the two
// experiments' logs can be compared without a translation table.
const (
	EventRunStarted      = "agent.run.started"
	EventPageInspected   = "page.inspected"
	EventDecided         = "agent.decided"
	EventActionRejected  = "browser.action.rejected"
	EventActionCompleted = "browser.action.completed"
	EventRunCompleted    = "agent.run.completed"
)

// Event is one line of a run's log.
type Event struct {
	TS              time.Time `json:"ts"`
	RunID           string    `json:"run_id,omitempty"`
	Step            int       `json:"step,omitempty"`
	Kind            string    `json:"kind"`
	SnapshotVersion int       `json:"snapshot_version,omitempty"`
	Nodes           int       `json:"nodes,omitempty"`
	Inferences      int       `json:"inferences,omitempty"`
	Ops             int       `json:"ops,omitempty"`
	DurationMS      int64     `json:"duration_ms,omitempty"`
	Detail          string    `json:"detail,omitempty"`
}

// EventWriter returns an emitter that writes one JSON object per line. A nil
// writer yields a no-op emitter, so a caller that wants no log passes nil.
func EventWriter(w io.Writer) func(Event) {
	if w == nil {
		return func(Event) {}
	}
	var mu sync.Mutex
	enc := json.NewEncoder(w)
	return func(e Event) {
		if e.TS.IsZero() {
			e.TS = time.Now().UTC()
		}
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(e)
	}
}

// MultiEmitter fans one event out to several emitters.
func MultiEmitter(emitters ...func(Event)) func(Event) {
	out := make([]func(Event), 0, len(emitters))
	for _, e := range emitters {
		if e != nil {
			out = append(out, e)
		}
	}
	return func(ev Event) {
		for _, e := range out {
			e(ev)
		}
	}
}
