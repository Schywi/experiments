package report

import (
	"fmt"
	"io"
	"sort"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// Group is one (class, pool, memory tier) cell of a run: what that combination
// was given, what it paid in swap, and what it cost in CPU and queue time. It is
// the answer to "which workload paid for the rebalance" (research/swap-capacity/01).
type Group struct {
	Class             string  `json:"class"`
	Pool              string  `json:"pool"`
	Tier              string  `json:"tier"`
	Sessions          int     `json:"sessions"`
	Swapped           int     `json:"sessions_swapped"`
	SwapP95MiB        float64 `json:"swap_p95_mib,omitempty"`
	SwapMaxMiB        float64 `json:"swap_max_mib,omitempty"`
	CPUCoreSecondsP50 float64 `json:"cpu_core_seconds_p50,omitempty"`
	QueueP95Ms        float64 `json:"queue_p95_ms,omitempty"`
	Failed            int     `json:"failed"`
}

// tierUnknown is the label a session carries when its instance row is missing or
// its template declared no annotation. It is shown rather than hidden, so an
// untiered pod reads as a gap in the table instead of merging into a tiered row.
const tierUnknown = "-"

// BuildGroups buckets sessions by the class and memory tier of the instance that
// served them, and by the pool the session was routed to. An instance row is the
// only place the class and tier are stored, so a session whose instance is absent
// lands in the unknown bucket.
func BuildGroups(sessions []model.Session, instances []model.Instance) []Group {
	byID := make(map[string]model.Instance, len(instances))
	for _, instance := range instances {
		byID[instance.ID] = instance
	}

	type acc struct {
		group Group
		swap  []float64
		cpu   []float64
		queue []float64
	}
	buckets := map[string]*acc{}
	order := make([]string, 0, len(sessions))

	for _, session := range sessions {
		instance, ok := byID[session.Instance]
		class := instance.Class
		tier := instance.SwapTier
		if !ok {
			class, tier = "", ""
		}
		if class == "" {
			class = "unknown"
		}
		if tier == "" {
			tier = tierUnknown
		}

		key := class + "\x00" + session.Pool + "\x00" + tier
		entry, seen := buckets[key]
		if !seen {
			entry = &acc{group: Group{Class: class, Pool: session.Pool, Tier: tier}}
			buckets[key] = entry
			order = append(order, key)
		}
		entry.group.Sessions++
		if session.Outcome != model.OutcomeSuccess {
			entry.group.Failed++
		}
		if session.SwapPeakBytes > 0 {
			entry.group.Swapped++
			entry.swap = append(entry.swap, float64(session.SwapPeakBytes)/(1<<20))
		}
		if session.CPUUsec > 0 {
			entry.cpu = append(entry.cpu, float64(session.CPUUsec)/1e6)
		}
		if session.ClaimCreatedNS > 0 && session.ReadyNS > session.ClaimCreatedNS {
			entry.queue = append(entry.queue, float64(session.ReadyNS-session.ClaimCreatedNS)/1e6)
		}
	}

	groups := make([]Group, 0, len(order))
	for _, key := range order {
		entry := buckets[key]
		if len(entry.swap) > 0 {
			entry.group.SwapP95MiB = percentile(entry.swap, 95)
			entry.group.SwapMaxMiB = percentile(entry.swap, 100)
		}
		if len(entry.cpu) > 0 {
			entry.group.CPUCoreSecondsP50 = percentile(entry.cpu, 50)
		}
		if len(entry.queue) > 0 {
			entry.group.QueueP95Ms = percentile(entry.queue, 95)
		}
		groups = append(groups, entry.group)
	}

	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Class != groups[j].Class {
			return groups[i].Class < groups[j].Class
		}
		if groups[i].Pool != groups[j].Pool {
			return groups[i].Pool < groups[j].Pool
		}
		return groups[i].Tier < groups[j].Tier
	})
	return groups
}

// printGroups writes the per-cell table. Nothing is printed when a run had no
// sessions, so an empty run does not read as a table of zeroes.
func (r *Report) printGroups(w io.Writer) {
	if len(r.Groups) == 0 {
		return
	}
	fmt.Fprintf(w, "\nper class/pool/tier (tier is the pod annotation session-lab.io/spill)\n")
	fmt.Fprintf(w, "  %-8s %-12s %-10s %5s %9s %10s %11s %10s %5s\n",
		"class", "pool", "tier", "sess", "swapped", "swap p95", "cpu core-s", "queue p95", "fail")
	for _, group := range r.Groups {
		fmt.Fprintf(w, "  %-8s %-12s %-10s %5d %9s %10s %11.2f %10.0f %5d\n",
			group.Class, group.Pool, group.Tier, group.Sessions,
			fmt.Sprintf("%d/%d", group.Swapped, group.Sessions),
			fmt.Sprintf("%.0f MiB", group.SwapP95MiB),
			group.CPUCoreSecondsP50, group.QueueP95Ms, group.Failed)
	}
}
