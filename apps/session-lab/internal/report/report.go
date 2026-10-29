// Package report turns a finished run's rows into the answers the experiment
// exists for: what completed, what failed, how long the queue was, what the
// sessions consumed, and which constraint broke first.
//
// Every number here comes from the run's own rows. Nothing is copied from a
// document, and a run that never reached a boundary says so.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/store"
)

// Report is the machine-readable result of one run.
type Report struct {
	RunID          string             `json:"run_id"`
	Config         model.RunConfig    `json:"config"`
	StartedNS      int64              `json:"started_ns,omitempty"`
	EndedNS        int64              `json:"ended_ns,omitempty"`
	Planned        int                `json:"planned_sessions"`
	Stored         int                `json:"stored_sessions"`
	Outcomes       map[string]int     `json:"outcomes"`
	Rate           float64            `json:"arrival_rate"`
	OfferedPeak    float64            `json:"offered_concurrency"`
	Concurrency    Concurrency        `json:"concurrency"`
	Queue          Latency            `json:"queue_wait_ms"`
	Acquire        Latency            `json:"acquire_ms"`
	Hold           Latency            `json:"hold_ms"`
	Reset          Latency            `json:"reset_ms"`
	Release        Latency            `json:"release_ms"`
	LaunchTypes    map[string]int     `json:"launch_types"`
	Buckets        map[string]Bucket  `json:"buckets"`
	Resources      Resources          `json:"resources"`
	Instances      InstanceSummary    `json:"instances"`
	Host           HostMemory         `json:"host_memory"`
	Template       *TemplateLimits    `json:"template_limits,omitempty"`
	Replicas       []store.ReplicaSample `json:"replicas,omitempty"`
	Boundary       string             `json:"first_boundary,omitempty"`
	BoundaryEv     []string           `json:"boundary_evidence,omitempty"`
	Elasticity     string             `json:"elasticity,omitempty"`
	Next           string             `json:"next,omitempty"`
	InvariantNotes []string           `json:"invariant_notes,omitempty"`
}

// Latency is a distribution in milliseconds.
type Latency struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
	Max   float64 `json:"max"`
}

// Concurrency is what the run actually held at once.
type Concurrency struct {
	Mean float64 `json:"mean"`
	Peak int     `json:"peak"`
}

// Bucket is one session-length class.
type Bucket struct {
	Sessions int     `json:"sessions"`
	OK       int     `json:"ok"`
	Failed   int     `json:"failed"`
	QueueP50 float64 `json:"queue_p50_ms"`
	HoldP50  float64 `json:"hold_p50_ms"`
}

// Resources aggregates the per-session counters.
type Resources struct {
	SessionsWithSamples int     `json:"sessions_with_samples"`
	CPUCoreSeconds      float64 `json:"cpu_core_seconds_total"`
	CPUCoreSecondsP50   float64 `json:"cpu_core_seconds_p50"`
	CPUCoreSecondsMax   float64 `json:"cpu_core_seconds_max"`
	MemPeakP50MiB       float64 `json:"mem_peak_p50_mib"`
	MemPeakMaxMiB       float64 `json:"mem_peak_max_mib"`
	WorkingSetPeakMaxMiB float64 `json:"mem_working_set_peak_max_mib"`
	ThrottledSessions   int     `json:"throttled_sessions"`
	OOMKills            int     `json:"oom_kills"`
	RefaultSessions     int     `json:"refault_sessions"`
}

// TemplateLimits is what the sandbox pods were held to. It is here so a
// throttling count can be read next to the limit that caused it.
type TemplateLimits struct {
	Container       string  `json:"container"`
	CPURequestMilli int64   `json:"cpu_request_milli"`
	CPULimitMilli   int64   `json:"cpu_limit_milli"`
	MemRequestMiB   float64 `json:"mem_request_mib"`
	MemLimitMiB     float64 `json:"mem_limit_mib"`
}

// HostMemory is the node's memory story for the run: the floor it reached and
// when, which is the difference between "the platform refused" and "the node ran
// out".
type HostMemory struct {
	Samples          int     `json:"samples"`
	TotalMiB         float64 `json:"total_mib"`
	MinAvailableMiB  float64 `json:"min_available_mib"`
	MinAtNS          int64   `json:"min_at_ns,omitempty"`
	MinRatioPct      float64 `json:"min_ratio_pct"`
	SwapUsedMiB      float64 `json:"swap_used_mib"`
}

// InstanceSummary counts instance reuse and rebuild cost.
type InstanceSummary struct {
	Total        int     `json:"total"`
	MaxSessions  int64   `json:"max_sessions_per_instance"`
	MeanSessions float64 `json:"mean_sessions_per_instance"`
	RebuildP50Ms float64 `json:"rebuild_p50_ms"`
}

// Build assembles a report from the store.
func Build(cfg model.RunConfig, plan schedule.Plan, st *store.Store) (*Report, error) {
	sessions, err := st.Sessions()
	if err != nil {
		return nil, err
	}
	instances, err := st.Instances()
	if err != nil {
		return nil, err
	}

	rep := &Report{
		RunID:       cfg.RunID,
		Config:      cfg,
		Planned:     len(plan.Sessions),
		Stored:      len(sessions),
		Outcomes:    map[string]int{},
		Rate:        plan.Rate,
		OfferedPeak: plan.PeakOffered(),
		LaunchTypes: map[string]int{},
		Buckets:     map[string]Bucket{},
		Elasticity:  "not tested: the workload did not exceed what the platform served",
	}

	var queueMS, acquireMS, holdMS, resetMS, releaseMS []float64
	var concurrencySamples []float64
	var cpuSeconds []float64
	var memPeaks, workSetPeaks []float64
	ok := 0
	failures := 0

	for _, session := range sessions {
		rep.Outcomes[string(session.Outcome)]++

		bucket := rep.Buckets[session.Bucket]
		bucket.Sessions++
		switch session.Outcome {
		case model.OutcomeSuccess:
			bucket.OK++
			ok++
		case model.OutcomeWaiting, model.OutcomePending:
		default:
			bucket.Failed++
			failures++
		}

		if session.ClaimCreatedNS > 0 && session.ReadyNS > 0 {
			queueMS = append(queueMS, ms(session.ReadyNS-session.ClaimCreatedNS))
			acquireMS = append(acquireMS, ms(session.ReadyNS-session.ArrivalNS))
		}
		if session.HoldEndNS > 0 && session.HoldStartNS > 0 {
			holdMS = append(holdMS, ms(session.HoldEndNS-session.HoldStartNS))
		}
		if session.ResetEndNS > 0 && session.ResetStartNS > 0 {
			resetMS = append(resetMS, ms(session.ResetEndNS-session.ResetStartNS))
		}
		if session.GoneNS > 0 && session.DeleteCalledNS > 0 {
			releaseMS = append(releaseMS, ms(session.GoneNS-session.DeleteCalledNS))
		}
		if session.LaunchType != "" {
			rep.LaunchTypes[session.LaunchType]++
		}
		if session.Samples > 0 {
			seconds := ms(session.HoldEndNS-session.HoldStartNS) / 1000
			core := float64(session.CPUUsec) / 1e6
			cpuSeconds = append(cpuSeconds, core)
			rep.Resources.SessionsWithSamples++
			rep.Resources.CPUCoreSeconds += core
			if session.ThrottledUsec > 0 {
				rep.Resources.ThrottledSessions++
			}
			if session.OOMKill > 0 {
				rep.Resources.OOMKills++
			}
			if session.RefaultAnon > 0 || session.RefaultFile > 0 {
				rep.Resources.RefaultSessions++
			}
			memPeaks = append(memPeaks, float64(session.MemPeakBytes)/(1<<20))
			workSetPeaks = append(workSetPeaks, float64(session.MemWorkingSetPeak)/(1<<20))
			// Concurrency for this session, integrated from its own window.
			if seconds > 0 {
				concurrencySamples = append(concurrencySamples, 1)
			}
		}
		rep.Buckets[session.Bucket] = bucket
	}

	rep.Queue = summarise(queueMS)
	rep.Acquire = summarise(acquireMS)
	rep.Hold = summarise(holdMS)
	rep.Reset = summarise(resetMS)
	rep.Release = summarise(releaseMS)

	rep.Concurrency = peakConcurrency(sessions)
	if len(cpuSeconds) > 0 {
		sort.Float64s(cpuSeconds)
		rep.Resources.CPUCoreSecondsP50 = percentile(cpuSeconds, 50)
		rep.Resources.CPUCoreSecondsMax = cpuSeconds[len(cpuSeconds)-1]
	}
	if len(memPeaks) > 0 {
		sort.Float64s(memPeaks)
		rep.Resources.MemPeakP50MiB = percentile(memPeaks, 50)
		rep.Resources.MemPeakMaxMiB = memPeaks[len(memPeaks)-1]
	}
	if len(workSetPeaks) > 0 {
		sort.Float64s(workSetPeaks)
		rep.Resources.WorkingSetPeakMaxMiB = workSetPeaks[len(workSetPeaks)-1]
	}

	rep.Instances = summariseInstances(instances)

	if raw, err := st.TemplateLimits(cfg.RunID); err == nil && raw != "" {
		var stored struct {
			Container       string `json:"container"`
			CPURequestMilli int64  `json:"cpu_request_milli"`
			CPULimitMilli   int64  `json:"cpu_limit_milli"`
			MemRequestBytes int64  `json:"mem_request_bytes"`
			MemLimitBytes   int64  `json:"mem_limit_bytes"`
		}
		if json.Unmarshal([]byte(raw), &stored) == nil {
			rep.Template = &TemplateLimits{
				Container:       stored.Container,
				CPURequestMilli: stored.CPURequestMilli,
				CPULimitMilli:   stored.CPULimitMilli,
				MemRequestMiB:   float64(stored.MemRequestBytes) / (1 << 20),
				MemLimitMiB:     float64(stored.MemLimitBytes) / (1 << 20),
			}
		}
	}

	hostSamples, err := st.HostSamples()
	if err == nil {
		rep.Host = summariseHost(hostSamples)
	}

	// Every counter that says the platform or the node pushed back becomes
	// evidence next to the first boundary.
	for _, session := range sessions {
		if session.OOMKill > 0 {
			rep.BoundaryEv = append(rep.BoundaryEv,
				fmt.Sprintf("session %d: oom_kill=%d", session.ID, session.OOMKill))
		}
		if session.ThrottledUsec > 0 {
			rep.BoundaryEv = append(rep.BoundaryEv,
				fmt.Sprintf("session %d: throttled_usec=%d (cpu limit reached)", session.ID, session.ThrottledUsec))
		}
	}

	// The boundary: the first session that the platform refused or that never
	// became ready, plus the event evidence the adapter saw.
	for _, session := range sessions {
		switch session.Outcome {
		case model.OutcomeAcquisitionFailed, model.OutcomeTimeout, model.OutcomeResetFailed,
			model.OutcomeDestroyFailed, model.OutcomeLostUnknown:
			if rep.Boundary == "" {
				rep.Boundary = fmt.Sprintf("%s at session %d: %s", session.Outcome, session.ID, session.Error)
				rep.BoundaryEv = append(rep.BoundaryEv, fmt.Sprintf("bucket %s, instance %s", session.Bucket, session.Instance))
			}
		}
	}
	if rep.Boundary != "" {
		rep.Elasticity = "exceeded: see first_boundary"
	}

	// Invariants: a run whose rows do not balance is not quotable.
	if ok+failures+rep.Outcomes[string(model.OutcomeWaiting)]+rep.Outcomes[string(model.OutcomePending)] != rep.Planned {
		rep.InvariantNotes = append(rep.InvariantNotes,
			fmt.Sprintf("outcome counts (%d) do not sum to the planned sessions (%d)", ok+failures, rep.Planned))
	}
	if rep.Stored != rep.Planned {
		rep.InvariantNotes = append(rep.InvariantNotes,
			fmt.Sprintf("stored sessions (%d) differ from planned (%d)", rep.Stored, rep.Planned))
	}

	rep.Next = recommend(rep)
	return rep, nil
}

// Write stores the report as JSON.
func (r *Report) Write(path string) error {
	payload, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(payload, '\n'), 0o644)
}

// Print writes the human summary.
func (r *Report) Print(w io.Writer) {
	fmt.Fprintf(w, "\nrun %s — %s policy, %d planned, %d stored\n", r.RunID, r.Config.Policy, r.Planned, r.Stored)
	fmt.Fprintf(w, "offered concurrency %.1f at %.3f sessions/s; measured peak %d, mean %.1f\n",
		r.OfferedPeak, r.Rate, r.Concurrency.Peak, r.Concurrency.Mean)
	fmt.Fprintln(w, "\noutcomes")
	for _, outcome := range []model.Outcome{model.OutcomeSuccess, model.OutcomeTimeout,
		model.OutcomeAcquisitionFailed, model.OutcomeResetFailed, model.OutcomeDestroyFailed,
		model.OutcomeLostUnknown, model.OutcomeWaiting, model.OutcomePending} {
		if count := r.Outcomes[string(outcome)]; count > 0 {
			fmt.Fprintf(w, "  %-20s %d\n", outcome, count)
		}
	}
	fmt.Fprintf(w, "\nqueue wait (claim→ready)  p50 %.0fms  p95 %.0fms  p99 %.0fms  max %.0fms\n",
		r.Queue.P50, r.Queue.P95, r.Queue.P99, r.Queue.Max)
	fmt.Fprintf(w, "acquire (arrival→ready)   p50 %.0fms  p95 %.0fms  p99 %.0fms\n",
		r.Acquire.P50, r.Acquire.P95, r.Acquire.P99)
	fmt.Fprintf(w, "hold                      p50 %.0fms  p95 %.0fms\n", r.Hold.P50, r.Hold.P95)
	if r.Reset.Count > 0 {
		fmt.Fprintf(w, "reset                     p50 %.0fms  p95 %.0fms\n", r.Reset.P50, r.Reset.P95)
	}
	if r.Release.Count > 0 {
		fmt.Fprintf(w, "release                   p50 %.0fms  p95 %.0fms\n", r.Release.P50, r.Release.P95)
	}
	fmt.Fprintf(w, "\nresources (sessions with samples: %d)\ncpu core-seconds  total %.2f  p50 %.2f  max %.2f\n",
		r.Resources.SessionsWithSamples, r.Resources.CPUCoreSeconds,
		r.Resources.CPUCoreSecondsP50, r.Resources.CPUCoreSecondsMax)
	fmt.Fprintf(w, "memory peak       p50 %.0f MiB  max %.0f MiB  working-set max %.0f MiB\n",
		r.Resources.MemPeakP50MiB, r.Resources.MemPeakMaxMiB, r.Resources.WorkingSetPeakMaxMiB)
	if r.Template != nil {
		fmt.Fprintf(w, "template %s      requests cpu=%dm mem=%.0fMiB, limits cpu=%dm mem=%.0fMiB\n",
			r.Template.Container, r.Template.CPURequestMilli, r.Template.MemRequestMiB,
			r.Template.CPULimitMilli, r.Template.MemLimitMiB)
	}
	fmt.Fprintf(w, "throttled sessions %d, oom kills %d, sessions with refaults %d\n",
		r.Resources.ThrottledSessions, r.Resources.OOMKills, r.Resources.RefaultSessions)
	if r.Host.Samples > 0 {
		fmt.Fprintf(w, "node memory       min available %.0f MiB of %.0f MiB (%.1f%%), swap used %.0f MiB, %d samples\n",
			r.Host.MinAvailableMiB, r.Host.TotalMiB, r.Host.MinRatioPct, r.Host.SwapUsedMiB, r.Host.Samples)
	}
	fmt.Fprintf(w, "\ninstances %d, sessions/instance max %d mean %.2f",
		r.Instances.Total, r.Instances.MaxSessions, r.Instances.MeanSessions)
	if r.Instances.RebuildP50Ms > 0 {
		fmt.Fprintf(w, ", rebuild p50 %.0fms", r.Instances.RebuildP50Ms)
	}
	fmt.Fprintf(w, "\nelasticity: %s\n", r.Elasticity)
	if r.Boundary != "" {
		fmt.Fprintf(w, "first boundary: %s\n", r.Boundary)
	}
	for _, note := range r.InvariantNotes {
		fmt.Fprintf(w, "invariant: %s\n", note)
	}
	fmt.Fprintf(w, "next: %s\n", r.Next)
}

func recommend(r *Report) string {
	switch {
	case r.Boundary != "":
		return "repeat with a lower arrival rate to confirm the boundary is repeatable, then re-run at this rate after changing the constraint above"
	case r.Concurrency.Peak == 0:
		return "no session recorded a concurrency sample; check that the sampler found the pod cgroups before trusting the resource numbers"
	case r.Resources.ThrottledSessions > 0:
		return "raise the CPU limit on the sandbox template, or remove it, and repeat: throttled sessions make hold latency an artifact of the limit"
	case r.Outcomes[string(model.OutcomeWaiting)] > 0:
		return "sessions were still waiting at the end: extend the run or raise the pool replica count and repeat"
	default:
		return "raise --overload-factor and repeat until the platform refuses, which is where the boundary is"
	}
}

func summarise(values []float64) Latency {
	if len(values) == 0 {
		return Latency{}
	}
	sort.Float64s(values)
	return Latency{
		Count: len(values),
		P50:   percentile(values, 50),
		P95:   percentile(values, 95),
		P99:   percentile(values, 99),
		Max:   values[len(values)-1],
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * p / 100)
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func ms(nanos int64) float64 { return float64(nanos) / 1e6 }

// peakConcurrency walks the session windows and returns what the run held at
// once. It is measured from the rows, not from the plan, so a run that never
// reached its offered load shows that.
func peakConcurrency(sessions []model.Session) Concurrency {
	type edge struct {
		at    int64
		delta int
	}
	var edges []edge
	var busyNanos int64
	var first, last int64
	for _, s := range sessions {
		start, end := s.ReadyNS, s.HoldEndNS
		if start == 0 || end == 0 || end <= start {
			continue
		}
		edges = append(edges, edge{start, 1}, edge{end, -1})
		busyNanos += end - start
		if first == 0 || start < first {
			first = start
		}
		if end > last {
			last = end
		}
	}
	if len(edges) == 0 {
		return Concurrency{}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].at == edges[j].at {
			return edges[i].delta < edges[j].delta
		}
		return edges[i].at < edges[j].at
	})
	current, peak := 0, 0
	for _, e := range edges {
		current += e.delta
		if current > peak {
			peak = current
		}
	}
	var mean float64
	if last > first {
		mean = float64(busyNanos) / float64(last-first)
	}
	return Concurrency{Mean: mean, Peak: peak}
}

func summariseHost(samples []model.HostSample) HostMemory {
	summary := HostMemory{Samples: len(samples)}
	if len(samples) == 0 {
		return summary
	}
	summary.TotalMiB = float64(samples[0].MemTotal) / (1 << 20)
	for _, sample := range samples {
		if summary.MinAvailableMiB == 0 || float64(sample.MemAvailable)/(1<<20) < summary.MinAvailableMiB {
			summary.MinAvailableMiB = float64(sample.MemAvailable) / (1 << 20)
			summary.MinAtNS = sample.AtNS
		}
		used := float64(sample.SwapTotal-sample.SwapFree) / (1 << 20)
		if used > summary.SwapUsedMiB {
			summary.SwapUsedMiB = used
		}
	}
	if summary.TotalMiB > 0 {
		summary.MinRatioPct = summary.MinAvailableMiB / summary.TotalMiB * 100
	}
	return summary
}

func summariseInstances(instances []model.Instance) InstanceSummary {
	summary := InstanceSummary{Total: len(instances)}
	if len(instances) == 0 {
		return summary
	}
	var totalSessions int64
	for _, instance := range instances {
		totalSessions += instance.Sessions
		if instance.Sessions > summary.MaxSessions {
			summary.MaxSessions = instance.Sessions
		}
	}
	summary.MeanSessions = float64(totalSessions) / float64(len(instances))
	return summary
}

// Duration returns the run's wall clock, when both ends were recorded.
func (r *Report) Duration() time.Duration {
	if r.StartedNS == 0 || r.EndedNS == 0 {
		return 0
	}
	return time.Duration(r.EndedNS - r.StartedNS)
}
