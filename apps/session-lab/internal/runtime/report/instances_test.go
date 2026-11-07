package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// TestBuildInstanceStatsCountsAttemptsAndFailures is the per-instance view the
// 3000-session runs needed: one row per browser, so "1378 failures on one
// instance" is legible from the report instead of requiring a database query.
func TestBuildInstanceStatsCountsAttemptsAndFailures(t *testing.T) {
	instances := []model.Instance{
		{ID: "inst-1", Class: "normal", SwapTier: "zram-2to1", Sessions: 2, BootCPUPeakCores: 1.5},
		{ID: "inst-2", Class: "normal", SwapTier: "zram-2to1", Sessions: 0, BootCPUPeakCores: 0.4},
	}
	sessions := []model.Session{
		{ID: 1, Instance: "inst-1", Pool: "browser-pool", Outcome: model.OutcomeSuccess, Attempts: 1},
		{ID: 2, Instance: "inst-1", Pool: "browser-pool", Outcome: model.OutcomeSuccess, Attempts: 2},
		{ID: 3, Instance: "inst-2", Pool: "browser-pool", Outcome: model.OutcomeResetFailed, Attempts: 1},
		{ID: 4, Instance: "inst-2", Pool: "browser-pool", Outcome: model.OutcomeResetFailed, Attempts: 2},
		{ID: 5, Instance: "inst-2", Pool: "browser-pool", Outcome: model.OutcomeLostUnknown, Attempts: 2},
		// A session whose instance row is gone still appears, under its own row.
		{ID: 6, Instance: "inst-gone", Pool: "browser-pool", Outcome: model.OutcomeSuccess, Attempts: 1},
	}

	stats := BuildInstanceStats(sessions, instances)
	if len(stats) != 3 {
		t.Fatalf("%d rows, want 3 (two known instances plus the orphan)", len(stats))
	}

	byID := map[string]InstanceStat{}
	for _, stat := range stats {
		byID[stat.Instance] = stat
	}

	one := byID["inst-1"]
	if one.Sessions != 2 || one.Succeeded != 2 || one.Failed != 0 {
		t.Fatalf("inst-1 = %+v, want 2 attempted and 2 succeeded", one)
	}
	if one.Attempts != 3 {
		t.Fatalf("inst-1 attempts = %d, want 3 (1 + 2)", one.Attempts)
	}
	if one.Tier != "zram-2to1" || one.BootCPUPeakCores != 1.5 {
		t.Fatalf("inst-1 tier/peak = %q/%.2f, want zram-2to1/1.5", one.Tier, one.BootCPUPeakCores)
	}

	two := byID["inst-2"]
	if two.Sessions != 3 || two.Succeeded != 0 || two.Failed != 3 {
		t.Fatalf("inst-2 = %+v, want 3 attempted and 3 failed", two)
	}

	orphan := byID["inst-gone"]
	if orphan.Sessions != 1 || orphan.Succeeded != 1 {
		t.Fatalf("orphan = %+v, want its single session counted", orphan)
	}
	if orphan.Tier != tierUnknown {
		t.Fatalf("orphan tier = %q, want %q", orphan.Tier, tierUnknown)
	}
}

// TestInstanceStatsArePrinted keeps the table in the human output.
func TestInstanceStatsArePrinted(t *testing.T) {
	rep := &Report{
		InstanceStats: []InstanceStat{
			{Instance: "inst-1", Class: "normal", Pool: "browser-pool", Tier: "zram-2to1",
				Sessions: 8, Succeeded: 8, Attempts: 9, BootCPUPeakCores: 1.2},
			{Instance: "inst-2", Class: "normal", Pool: "browser-pool", Tier: tierUnknown,
				Sessions: 3, Succeeded: 0, Failed: 3, Attempts: 6, BootCPUPeakCores: 0.3},
		},
	}
	var buffer bytes.Buffer
	rep.Print(&buffer)
	out := buffer.String()
	for _, want := range []string{"per instance", "inst-1", "inst-2", "8/8", "0/3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("printed report is missing %q:\n%s", want, out)
		}
	}
}
