package report

import (
	"testing"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
)

// TestBuildGroupsSplitsByClassPoolAndTier is the attribution claim in miniature:
// one run, two cells, one tiered and one untiered, and the table has to tell them
// apart instead of averaging them into one row.
func TestBuildGroupsSplitsByClassPoolAndTier(t *testing.T) {
	instances := []model.Instance{
		{ID: "cpu-1", Class: "normal", SwapTier: "zram-2to1", ContextsPerBrowser: 1},
		{ID: "gpu-1", Class: "normal", ContextsPerBrowser: 1},
	}
	sessions := []model.Session{
		{ID: 1, Instance: "cpu-1", Pool: "browser-pool", Outcome: model.OutcomeSuccess,
			SwapPeakBytes: 64 << 20, SwapAllowanceBytes: 64 << 20,
			CPUUsec: 4_210_000, ClaimCreatedNS: 1e9, ReadyNS: 1.097e9},
		{ID: 2, Instance: "cpu-1", Pool: "browser-pool", Outcome: model.OutcomeSuccess,
			SwapPeakBytes: 32 << 20, SwapAllowanceBytes: 64 << 20,
			CPUUsec: 4_000_000, ClaimCreatedNS: 1e9, ReadyNS: 1.101e9},
		{ID: 3, Instance: "gpu-1", Pool: "browser-pool-gpu", Outcome: model.OutcomeTimeout,
			CPUUsec: 8_100_000, ClaimCreatedNS: 1e9, ReadyNS: 1.210e9},
	}

	groups := BuildGroups(sessions, instances)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2 (one per cell)\n%+v", len(groups), groups)
	}

	tiered := groups[0]
	if tiered.Class != "normal" || tiered.Pool != "browser-pool" || tiered.Tier != "zram-2to1" {
		t.Fatalf("first group = %+v, want normal/browser-pool/zram-2to1", tiered)
	}
	if tiered.Sessions != 2 || tiered.Swapped != 2 || tiered.Failed != 0 {
		t.Fatalf("tiered counts = sessions %d swapped %d failed %d, want 2/2/0",
			tiered.Sessions, tiered.Swapped, tiered.Failed)
	}
	if tiered.SwapP95MiB < 60 || tiered.SwapP95MiB > 64 {
		t.Fatalf("tiered swap p95 = %.1f MiB, want ~64", tiered.SwapP95MiB)
	}
	if tiered.CPUCoreSecondsP50 < 4.0 || tiered.CPUCoreSecondsP50 > 4.3 {
		t.Fatalf("tiered cpu p50 = %.3f core-s, want ~4.1", tiered.CPUCoreSecondsP50)
	}
	if tiered.Allowance != "64 MiB" {
		t.Fatalf("tiered allowance = %q, want %q", tiered.Allowance, "64 MiB")
	}

	untiered := groups[1]
	if untiered.Pool != "browser-pool-gpu" || untiered.Tier != tierUnknown {
		t.Fatalf("second group = %+v, want browser-pool-gpu with tier %q", untiered, tierUnknown)
	}
	if untiered.Swapped != 0 || untiered.Failed != 1 {
		t.Fatalf("untiered counts = swapped %d failed %d, want 0/1", untiered.Swapped, untiered.Failed)
	}
}

// TestBuildGroupsMissingInstanceKeepsTheSession guards the silent-drop failure: a
// session whose instance row is gone still appears, as the unknown cell.
func TestBuildGroupsMissingInstanceKeepsTheSession(t *testing.T) {
	sessions := []model.Session{{ID: 7, Instance: "gone", Pool: "browser-pool", Outcome: model.OutcomeLostUnknown}}
	groups := BuildGroups(sessions, nil)
	if len(groups) != 1 || groups[0].Sessions != 1 {
		t.Fatalf("groups = %+v, want one bucket holding the orphan session", groups)
	}
	if groups[0].Tier != tierUnknown || groups[0].Class != "unknown" {
		t.Fatalf("orphan group = %+v, want class unknown and tier %q", groups[0], tierUnknown)
	}
}
