// Boundary detection: the observations that say the platform or the node
// refused, throttled or reclaimed something.
//
// These are separate records rather than inferences made later from latency, so
// a report can name the constraint that broke first instead of guessing it.
package measure

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// HostMemory is the node's memory picture from /proc/meminfo.
type HostMemory struct {
	TotalBytes     int64
	AvailableBytes int64
	SwapTotalBytes int64
	SwapFreeBytes  int64
}

// HostCPU is the node's CPU as a whole: cores in use, and how many exist. A pod's
// cgroup can only be read once it is Ready, so this is what makes overlapping
// startup spikes visible after the fact.
type HostCPU struct {
	UsedCores  float64
	TotalCores int
	AtNS       int64
}

// ReadHostCPU returns the node's average CPU use since boot, from /proc/stat.
// Two readings a second apart give the interval.
func ReadHostCPU() (HostCPU, error) {
	body, err := os.ReadFile("/proc/stat")
	if err != nil {
		return HostCPU{}, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		if len(fields) < 5 {
			break
		}
		values := make([]int64, 0, len(fields))
		for _, field := range fields {
			value, err := strconv.ParseInt(field, 10, 64)
			if err != nil {
				return HostCPU{}, err
			}
			values = append(values, value)
		}
		var total int64
		for _, value := range values {
			total += value
		}
		idle := values[3] + values[4] // idle + iowait
		cores := float64(runtime.NumCPU())
		return HostCPU{
			UsedCores:  float64(total-idle) / float64(total) * cores,
			TotalCores: runtime.NumCPU(),
			AtNS:       time.Now().UnixNano(),
		}, nil
	}
	return HostCPU{}, fmt.Errorf("no cpu line in /proc/stat")
}

// ReadHostMemory reads /proc/meminfo. It returns a zero value when the file is
// unavailable, which only happens off Linux.
func ReadHostMemory() HostMemory {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return HostMemory{}
	}
	defer file.Close()
	values := map[string]int64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		// /proc/meminfo is in KiB.
		values[key] = value * 1024
	}
	return HostMemory{
		TotalBytes:     values["MemTotal"],
		AvailableBytes: values["MemAvailable"],
		SwapTotalBytes: values["SwapTotal"],
		SwapFreeBytes:  values["SwapFree"],
	}
}

// Boundary is one refusal or degradation, with the evidence that produced it.
type Boundary struct {
	At     int64
	Kind   string
	Detail string
}

// Detector watches a run and reports what the platform refused.
//
// It never decides that a run should stop: it records. A threshold here is a
// reporting threshold, not a cap on the workload.
type Detector struct {
	// MemAvailableFloor is the lowest MemAvailable seen so far.
	MemAvailableFloor int64
	// MemTotal is the node's memory, used to express the floor as a ratio.
	MemTotal int64
	// Samples counts how many host readings were taken.
	Samples int

	firstAt int64
}

// Observe records one host reading.
func (d *Detector) Observe(atNanos int64, mem HostMemory) {
	d.Samples++
	if mem.TotalBytes > 0 {
		d.MemTotal = mem.TotalBytes
	}
	if mem.AvailableBytes > 0 && (d.MemAvailableFloor == 0 || mem.AvailableBytes < d.MemAvailableFloor) {
		d.MemAvailableFloor = mem.AvailableBytes
		if d.firstAt == 0 {
			d.firstAt = atNanos
		}
	}
}

// Boundaries returns the host-level findings, each phrased with the number that
// produced it.
func (d *Detector) Boundaries() []Boundary {
	var out []Boundary
	if d.MemAvailableFloor > 0 && d.MemTotal > 0 {
		ratio := float64(d.MemAvailableFloor) / float64(d.MemTotal)
		out = append(out, Boundary{
			At:     d.firstAt,
			Kind:   "host_mem_available_floor",
			Detail: fmt.Sprintf("MemAvailable fell to %d MiB of %d MiB (%.1f%%) over %d samples", d.MemAvailableFloor/(1<<20), d.MemTotal/(1<<20), ratio*100, d.Samples),
		})
	}
	return out
}

// SessionBoundaries turns one session's counters into boundary records: an OOM
// kill, and CPU throttling, each only when the counter is non-zero.
func SessionBoundaries(sessionID int64, oomKill, throttledUsec int64) []Boundary {
	var out []Boundary
	if oomKill > 0 {
		out = append(out, Boundary{
			Kind:   "session_oom_kill",
			Detail: fmt.Sprintf("session %d: memory.events oom_kill=%d", sessionID, oomKill),
		})
	}
	if throttledUsec > 0 {
		out = append(out, Boundary{
			Kind:   "session_cpu_throttled",
			Detail: fmt.Sprintf("session %d: cpu.stat throttled_usec=%d", sessionID, throttledUsec),
		})
	}
	return out
}
