package session

import "testing"

// TestBootPeakIsClampedToCoreCount is the run B anomaly made impossible: a pod
// cannot use more cores than the node has, so a sample that implies 15.6 cores on
// a 6-core box is a measurement error, not a finding.
func TestBootPeakIsClampedToCoreCount(t *testing.T) {
	// 15.6 cores worth of CPU time in one second, on a 6-core node.
	got := bootPeakCores(15_600_000, 1, 6)
	if got != 6 {
		t.Fatalf("boot peak = %.2f cores, want it clamped to 6", got)
	}

	// A run of the mill spike is reported as measured.
	if got := bootPeakCores(2_000_000, 1, 6); got != 2 {
		t.Fatalf("boot peak = %.2f cores, want 2", got)
	}

	// A sub-second window is still divided by its own length.
	if got := bootPeakCores(500_000, 0.25, 6); got != 2 {
		t.Fatalf("boot peak = %.2f cores, want 2", got)
	}

	// Nonsense in, zero out: no delta, no window, no reading.
	if got := bootPeakCores(0, 1, 6); got != 0 {
		t.Fatalf("boot peak = %.2f for a zero delta, want 0", got)
	}
	if got := bootPeakCores(1_000_000, 0, 6); got != 0 {
		t.Fatalf("boot peak = %.2f for a zero window, want 0", got)
	}

	// No limit means no clamp, which is how a machine that reports its own core
	// count as zero is handled.
	if got := bootPeakCores(15_600_000, 1, 0); got != 15.6 {
		t.Fatalf("boot peak = %.2f with no limit, want 15.6", got)
	}
}
