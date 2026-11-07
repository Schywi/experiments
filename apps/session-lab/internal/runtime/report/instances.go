package report

import (
	"fmt"
	"io"
	"sort"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// InstanceStat is one browser's contribution to a run: how many sessions were
// put on it, how many worked, and what it cost to start. It exists because run B's
// failures were all on one instance, and the report could not say so.
type InstanceStat struct {
	Instance         string  `json:"instance"`
	Class            string  `json:"class,omitempty"`
	Pool             string  `json:"pool,omitempty"`
	Tier             string  `json:"tier"`
	Sessions         int     `json:"sessions"`
	Succeeded        int     `json:"succeeded"`
	Failed           int     `json:"failed"`
	Attempts         int     `json:"attempts"`
	BootCPUPeakCores float64 `json:"boot_cpu_peak_cores,omitempty"`
}

// BuildInstanceStats groups the stored sessions by the instance that served them.
// A session whose instance row is missing keeps its own row, so a leaked instance
// does not disappear from the table.
func BuildInstanceStats(sessions []model.Session, instances []model.Instance) []InstanceStat {
	known := make(map[string]model.Instance, len(instances))
	for _, instance := range instances {
		known[instance.ID] = instance
	}

	order := make([]string, 0, len(instances))
	byID := make(map[string]*InstanceStat, len(instances))
	for _, session := range sessions {
		stat, seen := byID[session.Instance]
		if !seen {
			instance := known[session.Instance]
			stat = &InstanceStat{
				Instance:         session.Instance,
				Class:            instance.Class,
				Pool:             session.Pool,
				Tier:             instance.SwapTier,
				BootCPUPeakCores: instance.BootCPUPeakCores,
			}
			if stat.Tier == "" {
				stat.Tier = tierUnknown
			}
			byID[session.Instance] = stat
			order = append(order, session.Instance)
		}
		stat.Sessions++
		stat.Attempts += maxInt(session.Attempts, 1)
		if session.Outcome == model.OutcomeSuccess {
			stat.Succeeded++
		} else {
			stat.Failed++
		}
	}

	stats := make([]InstanceStat, 0, len(order))
	for _, id := range order {
		stats = append(stats, *byID[id])
	}
	// Most sessions first, then worst first: the instance that carried the run, and
	// the one that failed it, are both at the top.
	sort.SliceStable(stats, func(i, j int) bool {
		if stats[i].Sessions != stats[j].Sessions {
			return stats[i].Sessions > stats[j].Sessions
		}
		if stats[i].Failed != stats[j].Failed {
			return stats[i].Failed > stats[j].Failed
		}
		return stats[i].Instance < stats[j].Instance
	})
	return stats
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// printInstanceStats writes the per-instance table after the per-cell one.
func (r *Report) printInstanceStats(w io.Writer) {
	if len(r.InstanceStats) == 0 {
		return
	}
	fmt.Fprintf(w, "\nper instance (ok/attempted; a row with 0/N is a browser that never served)\n")
	fmt.Fprintf(w, "  %-28s %-10s %9s %10s %8s\n", "instance", "tier", "sessions", "attempts", "boot c")
	for _, stat := range r.InstanceStats {
		fmt.Fprintf(w, "  %-28s %-10s %9s %10d %8.2f\n",
			stat.Instance, stat.Tier,
			fmt.Sprintf("%d/%d", stat.Succeeded, stat.Sessions),
			stat.Attempts, stat.BootCPUPeakCores)
	}
}
