package measure

// Node-wide inputs for the pool autoscaler: CPU PSI full pressure and interval
// CPU utilisation. Both read /proc directly because the autoscaler runs on the
// host, which is also the node on this cluster.

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// ReadHostCPUPressureFull returns the CPU PSI "full avg10" value as a percentage
// (0..100): the share of the last ten seconds in which every runnable task was
// stalled waiting for a core. /proc/pressure/cpu.
func ReadHostCPUPressureFull() (float64, error) {
	body, err := os.ReadFile("/proc/pressure/cpu")
	if err != nil {
		return 0, err
	}
	return parsePressureFullAvg10(string(body))
}

// parsePressureFullAvg10 extracts "full avg10" from a PSI file body.
func parsePressureFullAvg10(body string) (float64, error) {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "full" {
			continue
		}
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(field, "=")
			if ok && key == "avg10" {
				return strconv.ParseFloat(value, 64)
			}
		}
	}
	return 0, fmt.Errorf("no full avg10 line in pressure file")
}

// NodeCPUUtilization computes instantaneous CPU utilisation from the delta
// between two /proc/stat readings. The first call has no previous sample, so it
// falls back to the since-boot average; every later call is the interval since
// the call before it.
type NodeCPUUtilization struct {
	mu        sync.Mutex
	lastTotal int64
	lastIdle  int64
}

// Utilization returns the fraction of the node's CPU in use (0..1) plus the
// number of cores.
func (n *NodeCPUUtilization) Utilization() (float64, int, error) {
	body, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	total, idle, err := parseCPUStat(string(body))
	if err != nil {
		return 0, 0, err
	}
	cores := runtime.NumCPU()

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.lastTotal == 0 {
		n.lastTotal, n.lastIdle = total, idle
		return clamp01(1 - float64(idle)/float64(total)), cores, nil
	}
	dt := total - n.lastTotal
	di := idle - n.lastIdle
	n.lastTotal, n.lastIdle = total, idle
	if dt <= 0 {
		return 0, cores, nil
	}
	return clamp01(1 - float64(di)/float64(dt)), cores, nil
}

// parseCPUStat returns total and idle+wait ticks for the aggregate CPU line.
func parseCPUStat(body string) (total, idle int64, err error) {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		if len(fields) < 5 {
			return 0, 0, fmt.Errorf("short cpu line in /proc/stat")
		}
		values := make([]int64, 0, len(fields))
		for _, field := range fields {
			value, err := strconv.ParseInt(field, 10, 64)
			if err != nil {
				return 0, 0, err
			}
			values = append(values, value)
		}
		for _, value := range values {
			total += value
		}
		idle = values[3] + values[4] // idle + iowait
		return total, idle, nil
	}
	return 0, 0, fmt.Errorf("no cpu line in /proc/stat")
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
