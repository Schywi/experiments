// Package measure reads per-sandbox resource counters from cgroup v2 and host
// pressure from /proc/pressure.
//
// The choice of instrument is argued in
// research/session-lifecycle-elasticity/01-instrumentation-cpu-memory.md: the
// cgroup files answer "how much CPU did this sandbox use and was it throttled",
// and the pressure files answer "was it runnable but starved". Both are read from
// the host, which is also the node on this cluster.
package measure

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	kube "github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/kube"
)

// Options configures the sampler.
type Options struct {
	// CgroupRoot is the cgroup v2 mount point. Empty detects /sys/fs/cgroup.
	CgroupRoot string
	// Interval is the intended spacing between samples; it is recorded so a
	// report can say how dense the series was.
	Interval time.Duration
	Log      *log.Logger
}

// Sampler implements the session runner's Sampler interface.
type Sampler struct {
	root  string
	log   *log.Logger
	mu    sync.Mutex
	dirs  map[string]string
	hosts []string
}

// New builds a sampler. A host without cgroup v2 yields a sampler that reports
// nothing rather than an error, so a run still records lifecycle timings.
func New(opts Options) (*Sampler, error) {
	root := opts.CgroupRoot
	if root == "" {
		root = kube.NodePathForCgroup()
	}
	logger := opts.Log
	if logger == nil {
		logger = log.Default()
	}
	s := &Sampler{root: root, log: logger, dirs: map[string]string{}}
	if root == "" {
		logger.Printf("measure: no cgroup v2 mount found; resource sampling disabled")
		return s, nil
	}
	if _, err := os.Stat(filepath.Join(root, "kubepods.slice")); err != nil {
		logger.Printf("measure: %s has no kubepods.slice; resource sampling disabled", root)
		s.root = ""
	}
	return s, nil
}

// Sample reads one sandbox pod's counters.
func (s *Sampler) Sample(ctx context.Context, podUID, node string) (Reading, bool, error) {
	if s.root == "" || podUID == "" {
		return Reading{}, false, nil
	}
	dir, err := s.dirFor(podUID)
	if err != nil {
		// A pod whose cgroup is not there yet is a miss, not a failure: the run
		// keeps its lifecycle timings and the report says how many sessions had
		// samples.
		return Reading{}, false, nil
	}
	reading := Reading{}

	cpu, err := readKV(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		return Reading{}, false, err
	}
	reading.CPUUsec = cpu["usage_usec"]
	reading.ThrottledUsec = cpu["throttled_usec"]
	reading.NRThrottled = cpu["nr_throttled"]

	if pressure, err := readPressure(filepath.Join(dir, "cpu.pressure")); err == nil {
		reading.CPUPressureSomeUsec = pressure["some"]
		reading.CPUPressureFullUsec = pressure["full"]
	}
	if pressure, err := readPressure(filepath.Join(dir, "memory.pressure")); err == nil {
		reading.MemPressureSomeUsec = pressure["some"]
	}

	mem, err := readKV(filepath.Join(dir, "memory.stat"))
	if err != nil {
		return Reading{}, false, err
	}
	reading.MemInactiveFile = mem["inactive_file"]
	reading.Shmem = mem["shmem"]
	reading.Anon = mem["anon"]
	reading.RefaultAnon = mem["workingset_refault_anon"]
	reading.RefaultFile = mem["workingset_refault_file"]

	if current, err := readInt(filepath.Join(dir, "memory.current")); err == nil {
		reading.MemCurrent = current
		// working set is what OOM and eviction act on: current minus the
		// reclaimable inactive file pages.
		reading.MemWorkingSet = current - mem["inactive_file"]
		if reading.MemWorkingSet < 0 {
			reading.MemWorkingSet = 0
		}
	}
	if swap, err := readInt(filepath.Join(dir, "memory.swap.current")); err == nil {
		reading.SwapCurrent = swap
	}
	// memory.swap.max is the allowance this pod was actually given, and the only
	// number that decides whether it can swap at all. The value that matters is
	// the container scope's, not the pod slice's: kubelet writes 0 on the
	// container and leaves the slice at "max", and a child cannot exceed its
	// parent, so reading the slice would report an allowance the browser does not
	// have. "max" at both levels is -1, meaning unconstrained.
	if allowance, ok := swapAllowance(dir); ok {
		reading.SwapMax = allowance
	}
	if events, err := readKV(filepath.Join(dir, "memory.events")); err == nil {
		reading.OOMKill = events["oom_kill"]
	}
	return reading, true, nil
}

func (s *Sampler) dirFor(podUID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir, ok := s.dirs[podUID]; ok {
		return dir, nil
	}
	dir, err := kube.PodCgroupDir(s.root, podUID)
	if err != nil {
		return "", err
	}
	s.dirs[podUID] = dir
	return dir, nil
}

// HostPressure reads the host-wide pressure stall information.
func (s *Sampler) HostPressure() map[string]map[string]int64 {
	out := map[string]map[string]int64{}
	for _, resource := range []string{"cpu", "memory", "io"} {
		values, err := readPressure(filepath.Join("/proc/pressure", resource))
		if err != nil {
			continue
		}
		out[resource] = values
	}
	return out
}

// readKV parses a "key value" cgroup file into integers.
func readKV(path string) (map[string]int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	out := map[string]int64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		out[fields[0]] = value
	}
	return out, scanner.Err()
}

// swapAllowance reads the memory.swap.max that applies to a pod's processes. The
// container scopes are what kubelet writes and what the browser actually runs
// under, so they win over the pod slice. The widest value across scopes is taken:
// every container in this pod is the browser or its sidecar, and the sidecar's
// own allowance must not hide the browser's. -1 means "max" at every level read.
func swapAllowance(podDir string) (int64, bool) {
	read := func(path string) (int64, bool) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return 0, false
		}
		text := strings.TrimSpace(string(raw))
		if text == "max" {
			return -1, true
		}
		value, convErr := strconv.ParseInt(text, 10, 64)
		if convErr != nil {
			return 0, false
		}
		return value, true
	}

	scopes, _ := filepath.Glob(filepath.Join(podDir, "*.scope", "memory.swap.max"))
	var best int64
	foundFinite, sawMax := false, false
	for _, path := range scopes {
		value, ok := read(path)
		if !ok {
			continue
		}
		if value < 0 {
			// A scope that says "max" is not the browser's container: kubelet
			// writes a number on every container it starts, and the sandbox's
			// own process shows up here too. Keep it only as a fallback.
			sawMax = true
			continue
		}
		if !foundFinite || value < best {
			best, foundFinite = value, true
		}
	}
	if foundFinite {
		return best, true
	}
	if sawMax {
		return -1, true
	}
	return read(filepath.Join(podDir, "memory.swap.max"))
}

func readInt(path string) (int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
}

// readPressure parses a PSI file: "some avg10=0.00 ... total=12345".
func readPressure(path string) (map[string]int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	out := map[string]int64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		kind := fields[0]
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(field, "=")
			if !ok || key != "total" {
				continue
			}
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				continue
			}
			out[kind] = parsed
		}
	}
	return out, scanner.Err()
}

// String describes the sampler for a log line.
func (s *Sampler) String() string {
	if s.root == "" {
		return "measure: disabled"
	}
	return fmt.Sprintf("measure: cgroup v2 at %s", s.root)
}

// The runner depends on this shape (a Sample method); keeping the assertion here
// would make the measurement layer import the runtime, so it lives there instead.

// Reading is one resource sample for a sandbox pod.
type Reading struct {
	CPUUsec             int64
	CPUPressureSomeUsec int64
	CPUPressureFullUsec int64
	ThrottledUsec       int64
	NRThrottled         int64
	MemCurrent          int64
	MemWorkingSet       int64
	MemInactiveFile     int64
	Shmem               int64
	Anon                int64
	RefaultAnon         int64
	RefaultFile         int64
	SwapCurrent         int64
	// SwapMax is the pod's memory.swap.max: the allowance it was given, in bytes,
	// or -1 when the cgroup says "max" (unconstrained). Zero means no swap.
	SwapMax             int64
	OOMKill             int64
	MemPressureSomeUsec int64
}
