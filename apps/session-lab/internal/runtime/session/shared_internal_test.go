package session

import "testing"

// The cgroup counter belongs to the pod, so a per-session row carries its share of
// both CPU and memory. Memory was missed, which made 30 sessions of 339 MiB each
// read as ~10 GiB of browser footprint.
func TestDivideReadingSharesCpuAndMemory(t *testing.T) {
	reading := Reading{CPUUsec: 4000, MemCurrent: 400 << 20, MemWorkingSet: 300 << 20}
	divided := divideReading(reading, 4)
	if divided.CPUUsec != 1000 {
		t.Fatalf("cpu = %d, want a quarter of the pod's", divided.CPUUsec)
	}
	if divided.MemCurrent != 100<<20 || divided.MemWorkingSet != 75<<20 {
		t.Fatalf("memory = %d/%d, want a quarter of the pod's 400/300 MiB",
			divided.MemCurrent, divided.MemWorkingSet)
	}

	// An instance serving one session is not divided at all.
	alone := divideReading(reading, 1)
	if alone.MemCurrent != reading.MemCurrent || alone.CPUUsec != reading.CPUUsec {
		t.Fatalf("a single-session browser was divided anyway: %+v", alone)
	}
}
