package measure

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeCgroup builds a synthetic pod cgroup so the reader can be tested without
// a cluster.
func writeCgroup(t *testing.T, root, podUID string) string {
	t.Helper()
	dir := filepath.Join(root, "kubepods.slice", "kubepods-besteffort.slice", "cri-containerd-"+podUID+".scope")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := map[string]string{
		"cpu.stat":        "usage_usec 1500000\nuser_usec 1000000\nsystem_usec 500000\nnr_periods 100\nnr_throttled 7\nthrottled_usec 250000\n",
		"cpu.pressure":    "some avg10=1.00 avg60=2.00 avg300=0.50 total=123456\nfull avg10=0.50 avg60=1.00 avg300=0.25 total=65432\n",
		"memory.current":  "104857600\n",
		"memory.stat":     "anon 52428800\nfile 10485760\ninactive_file 4194304\nshmem 2097152\nworkingset_refault_anon 11\nworkingset_refault_file 22\npgscan 33\npgsteal 44\n",
		"memory.swap.current": "8388608\n",
		"memory.events":   "low 0\nhigh 3\nmax 1\noom 0\noom_kill 1\n",
		"memory.pressure": "some avg10=0.10 avg60=0.20 avg300=0.30 total=777\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=88\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestSampleReadsEveryCounter(t *testing.T) {
	root := t.TempDir()
	writeCgroup(t, root, "abc12345-0000-1111-2222-333344445555")

	sampler, err := New(Options{CgroupRoot: root})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	reading, ok, err := sampler.Sample(context.Background(), "abc12345-0000-1111-2222-333344445555", "node")
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if !ok {
		t.Fatalf("sample reported nothing")
	}
	if reading.CPUUsec != 1500000 || reading.ThrottledUsec != 250000 || reading.NRThrottled != 7 {
		t.Fatalf("cpu counters wrong: %+v", reading)
	}
	if reading.CPUPressureSomeUsec != 123456 || reading.CPUPressureFullUsec != 65432 {
		t.Fatalf("cpu pressure wrong: %+v", reading)
	}
	// working set is current minus inactive_file, the number OOM acts on.
	if want := int64(104857600 - 4194304); reading.MemWorkingSet != want {
		t.Fatalf("working set = %d, want %d", reading.MemWorkingSet, want)
	}
	if reading.MemCurrent != 104857600 || reading.MemInactiveFile != 4194304 {
		t.Fatalf("memory gauges wrong: %+v", reading)
	}
	if reading.RefaultAnon != 11 || reading.RefaultFile != 22 {
		t.Fatalf("refaults wrong: %+v", reading)
	}
	if reading.OOMKill != 1 {
		t.Fatalf("oom_kill = %d, want 1", reading.OOMKill)
	}
	if reading.SwapCurrent != 8388608 {
		t.Fatalf("swap current = %d", reading.SwapCurrent)
	}
	if reading.MemPressureSomeUsec != 777 {
		t.Fatalf("memory pressure = %d", reading.MemPressureSomeUsec)
	}
}

func TestSampleWithoutCgroupReportsNothing(t *testing.T) {
	sampler, err := New(Options{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// An unknown pod UID is a miss, not an error: the run keeps its lifecycle
	// timings even when the sampler cannot see the pod.
	_, ok, err := sampler.Sample(context.Background(), "missing-pod-uid", "node")
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if ok {
		t.Fatalf("sampler claimed a reading for a pod that does not exist")
	}
}

func TestQuantityParsing(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
	}{
		{"12m", 12},
		{"1", 1000},
		{"1500m", 1500},
		{"0", 0},
	}
	for _, c := range cases {
		if got := parseQuantityMilli(c.raw); got != c.want {
			t.Fatalf("parseQuantityMilli(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
	bytes := []struct {
		raw  string
		want int64
	}{
		{"100Mi", 100 << 20},
		{"1Gi", 1 << 30},
		{"250Ki", 250 << 10},
	}
	for _, c := range bytes {
		if got := parseQuantityBytes(c.raw); got != c.want {
			t.Fatalf("parseQuantityBytes(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}

func TestDetectorReportsTheFloorItSaw(t *testing.T) {
	detector := &Detector{}
	detector.Observe(100, HostMemory{TotalBytes: 8 << 30, AvailableBytes: 6 << 30})
	detector.Observe(200, HostMemory{TotalBytes: 8 << 30, AvailableBytes: 1 << 30})
	detector.Observe(300, HostMemory{TotalBytes: 8 << 30, AvailableBytes: 2 << 30})

	if detector.MemAvailableFloor != 1<<30 {
		t.Fatalf("floor = %d, want %d", detector.MemAvailableFloor, int64(1<<30))
	}
	boundaries := detector.Boundaries()
	if len(boundaries) != 1 {
		t.Fatalf("want 1 boundary, got %d", len(boundaries))
	}
	if boundaries[0].At != 100 {
		t.Fatalf("boundary time = %d, want the first sample at 100", boundaries[0].At)
	}
}

func TestSessionBoundariesOnlyWhenTheCounterMoved(t *testing.T) {
	if got := SessionBoundaries(1, 0, 0); len(got) != 0 {
		t.Fatalf("a clean session produced %d boundaries", len(got))
	}
	if got := SessionBoundaries(2, 1, 5000); len(got) != 2 {
		t.Fatalf("want an oom and a throttle boundary, got %d", len(got))
	}
}

func TestReadHostMemory(t *testing.T) {
	memory := ReadHostMemory()
	if memory.TotalBytes <= 0 {
		t.Skip("no /proc/meminfo on this platform")
	}
	if memory.AvailableBytes <= 0 {
		t.Fatalf("MemAvailable read as %d", memory.AvailableBytes)
	}
}
