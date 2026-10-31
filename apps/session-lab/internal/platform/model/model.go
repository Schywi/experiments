// Package model holds the records a run produces.
//
// Nothing here carries a default that stands in for a measurement: a session
// starts with zero timestamps and zero counters, and the zero value means "not
// observed yet" rather than "zero".
package model

import (
	"fmt"
	"time"
)

// Outcome is the terminal state of one session. The set is fixed so the values
// can be counted and used as a label.
type Outcome string

const (
	// OutcomePending is a session the scheduler has not finished with.
	OutcomePending Outcome = "pending"
	// OutcomeWaiting is a session still queued for an instance when the run
	// ended. It is reportable on its own and never folded into a failure count.
	OutcomeWaiting Outcome = "waiting"
	// OutcomeSuccess ran to completion and released its instance.
	OutcomeSuccess Outcome = "success"
	// OutcomeTimeout exceeded the operator's per-session deadline.
	OutcomeTimeout Outcome = "timeout"
	// OutcomeAcquisitionFailed never got an instance.
	OutcomeAcquisitionFailed Outcome = "acquisition_failed"
	// OutcomeResetFailed could not be reset for the next session.
	OutcomeResetFailed Outcome = "reset_failed"
	// OutcomeDestroyFailed could not be torn down.
	OutcomeDestroyFailed Outcome = "destroy_failed"
	// OutcomeLostUnknown ended with no observed terminal transition.
	OutcomeLostUnknown Outcome = "lost_unknown"
)

// Failures lists the outcomes the report counts as failures. OutcomeWaiting and
// OutcomePending are not failures; a session still queued is its own row.
var Failures = []Outcome{
	OutcomeTimeout,
	OutcomeAcquisitionFailed,
	OutcomeResetFailed,
	OutcomeDestroyFailed,
	OutcomeLostUnknown,
}

// Class is the workload class an operator assigns to a run. It decides queue
// order and whether the run's claims count as demand for the pool. It never
// rejects anything.
type Class string

const (
	ClassNormal Class = "normal"
	ClassBatch  Class = "batch"
	ClassBulk   Class = "bulk"
)

// ParseClass validates a --class value.
func ParseClass(value string) (Class, error) {
	switch Class(value) {
	case ClassNormal, ClassBatch, ClassBulk:
		return Class(value), nil
	case "":
		return ClassNormal, nil
	}
	return "", fmt.Errorf("unknown class %q: want normal, batch or bulk", value)
}

// Rank orders the queue: lower is served first.
func (c Class) Rank() int {
	switch c {
	case ClassNormal:
		return 0
	case ClassBatch:
		return 1
	case ClassBulk:
		return 2
	}
	return 0
}

// MayScale reports whether demand from this class may grow the pool. Bulk work
// uses capacity that already exists and never asks for more.
func (c Class) MayScale() bool { return c != ClassBulk }

// Interruptible reports whether a session of this class may be suspended to make
// room. v1 does not preempt; the policy lives here so it is stated once.
func (c Class) Interruptible() bool { return c == ClassBulk }

// Policy is the instance lifecycle a run exercises.
type Policy string

const (
	// PolicyReuse keeps one instance and resets it between sessions.
	PolicyReuse Policy = "reuse"
	// PolicyDestroy deletes the instance after every session.
	PolicyDestroy Policy = "destroy"
	// PolicySuspend suspends the instance between sessions.
	PolicySuspend Policy = "suspend"
)

// Session is one simulated agent's life, from arrival to terminal outcome.
//
// Every timestamp is unix nanoseconds and zero means "not observed". The store
// writes each transition as it happens, so a run killed mid-flight still has
// every row that was reached.
type Session struct {
	ID        int64   `json:"id"`
	Bucket    string  `json:"bucket"`
	Policy    Policy  `json:"policy"`
	Namespace string  `json:"namespace"`
	Template  string  `json:"template"`
	Pool      string  `json:"pool"`
	Instance  string  `json:"instance,omitempty"`
	Outcome   Outcome `json:"outcome"`
	Error     string  `json:"error,omitempty"`

	ArrivalNS int64 `json:"arrival_ns"`
	// AdmissionWaitNS is the time this session waited for its class to be
	// admitted, before any cluster call.
	AdmissionWaitNS int64 `json:"admission_wait_ns,omitempty"`
	// CPUShared is set when this session shared its Chromium with others, so
	// the per-session CPU figure is the pod's usage divided by the contexts in
	// flight rather than an observation of this session alone.
	CPUShared bool `json:"cpu_shared,omitempty"`
	// BrowserContext and Target identify the context this session ran in.
	BrowserContext string `json:"browser_context,omitempty"`
	// Per-session cost, measured inside the browser on the session's own target.
	// TaskDurationUS is main-thread time, not process CPU: it belongs beside
	// CPUUsec, never added to it. JSHeapBytes is this context's heap, which is an
	// exact per-session number even when the browser is shared.
	TaskDurationUS   int64 `json:"task_duration_us,omitempty"`
	ScriptDurationUS int64 `json:"script_duration_us,omitempty"`
	JSHeapBytes      int64 `json:"js_heap_bytes,omitempty"`
	ClaimCreatedNS   int64 `json:"claim_created_ns,omitempty"`
	ReadyNS          int64 `json:"ready_ns,omitempty"`
	HoldStartNS      int64 `json:"hold_start_ns,omitempty"`
	HoldEndNS        int64 `json:"hold_end_ns,omitempty"`
	DeleteCalledNS   int64 `json:"delete_called_ns,omitempty"`
	GoneNS           int64 `json:"gone_ns,omitempty"`
	ResetStartNS     int64 `json:"reset_start_ns,omitempty"`
	ResetEndNS       int64 `json:"reset_end_ns,omitempty"`
	RebuildStartNS   int64 `json:"rebuild_start_ns,omitempty"`
	RebuildEndNS     int64 `json:"rebuild_end_ns,omitempty"`
	ResumeStartNS    int64 `json:"resume_start_ns,omitempty"`
	ResumeEndNS      int64 `json:"resume_end_ns,omitempty"`

	LaunchType string `json:"launch_type,omitempty"`
	Node       string `json:"node,omitempty"`
	PodUID     string `json:"pod_uid,omitempty"`

	CPUUsec             int64 `json:"cpu_usec,omitempty"`
	CPUPressureSomeUsec int64 `json:"cpu_pressure_some_usec,omitempty"`
	CPUPressureFullUsec int64 `json:"cpu_pressure_full_usec,omitempty"`
	ThrottledUsec       int64 `json:"throttled_usec,omitempty"`
	NRThrottled         int64 `json:"nr_throttled,omitempty"`
	MemPeakBytes        int64 `json:"mem_peak_bytes,omitempty"`
	MemWorkingSetPeak   int64 `json:"mem_working_set_peak_bytes,omitempty"`
	MemInactiveFile     int64 `json:"mem_inactive_file_bytes,omitempty"`
	ShmemPeak           int64 `json:"shmem_peak_bytes,omitempty"`
	AnonPeak            int64 `json:"anon_peak_bytes,omitempty"`
	RefaultAnon         int64 `json:"workingset_refault_anon,omitempty"`
	RefaultFile         int64 `json:"workingset_refault_file,omitempty"`
	SwapPeakBytes       int64 `json:"swap_peak_bytes,omitempty"`
	OOMKill             int64 `json:"oom_kill,omitempty"`
	MemPressureSomeUsec int64 `json:"mem_pressure_some_usec,omitempty"`
	Samples             int64 `json:"samples,omitempty"`
}

// Instance is one sandbox the run created or adopted, keyed to the session that
// held it. A reused instance appears with several sessions.
type Instance struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Node      string `json:"node,omitempty"`
	PodUID    string `json:"pod_uid,omitempty"`
	CreatedNS int64  `json:"created_ns,omitempty"`
	ReadyNS   int64  `json:"ready_ns,omitempty"`
	DeletedNS int64  `json:"deleted_ns,omitempty"`
	Suspends  int64  `json:"suspends,omitempty"`
	Resumes   int64  `json:"resumes,omitempty"`
	Resets    int64  `json:"resets,omitempty"`
	Sessions  int64  `json:"sessions,omitempty"`
}

// Event is one lifecycle transition, kept alongside the session row so a run can
// be replayed in order after the fact.
type Event struct {
	ID        int64  `json:"-"`
	SessionID int64  `json:"session,omitempty"`
	AtNS      int64  `json:"at_ns"`
	Kind      string `json:"kind"`
	Detail    string `json:"detail,omitempty"`
}

// HostSample is one reading of the node's memory while the run was in progress.
// It is what says whether the run reached the node's limit, and when.
type HostSample struct {
	AtNS         int64 `json:"at_ns"`
	MemTotal     int64 `json:"mem_total"`
	MemAvailable int64 `json:"mem_available"`
	SwapTotal    int64 `json:"swap_total,omitempty"`
	SwapFree     int64 `json:"swap_free,omitempty"`
}

// Bucket is one session-length class: a hold duration and how many sessions of
// that length the run should generate.
type Bucket struct {
	Label string        `json:"label"`
	Hold  time.Duration `json:"hold_ns"`
	Count int           `json:"count"`
}

// RunConfig is everything the operator chose for this run. A zero value for
// Sessions, Workers or PoolReplicas means "take it from the run configuration
// file or the command line"; the runner never substitutes a remembered number
// for any of them.
type RunConfig struct {
	RunID        string   `json:"run_id"`
	Sessions     int      `json:"sessions"`
	Rate         float64  `json:"rate"`
	FixedArrival bool     `json:"fixed_arrival"`
	Mix          []Bucket `json:"mix,omitempty"`
	Policy       Policy   `json:"policy"`
	Class        Class    `json:"class,omitempty"`
	Namespace    string   `json:"namespace"`
	Pool         string   `json:"pool"`
	Template     string   `json:"template"`
	PoolReplicas *int32   `json:"pool_replicas,omitempty"`
	Workers      int      `json:"workers"`
	Admit        int      `json:"admit,omitempty"`
	// ContextsPerBrowser is how many sessions one Chromium serves. One keeps
	// the historical one-claim-per-session shape; more turns each session into
	// an isolated browser context inside a shared browser.
	ContextsPerBrowser int `json:"contexts_per_browser,omitempty"`
	// TargetURL is the page a session starts on, inside its own context.
	TargetURL string `json:"target_url,omitempty"`
	// CDPTransport is "http" for the in-pod bridge or "ws" for a direct
	// DevTools connection; CDPPort is the port the sidecar publishes.
	CDPTransport    string        `json:"cdp_transport,omitempty"`
	CDPPort         int           `json:"cdp_port,omitempty"`
	OverloadFactor  float64       `json:"overload_factor,omitempty"`
	SessionDeadline time.Duration `json:"session_deadline_ns,omitempty"`
	// Window is the intended arrival window when the operator asked for one.
	Window        time.Duration `json:"arrival_window_ns,omitempty"`
	SampleSeconds float64       `json:"sample_seconds"`
	CgroupRoot    string        `json:"cgroup_root,omitempty"`
	OutDir        string        `json:"out_dir"`
	DryRun        bool          `json:"dry_run"`
}
