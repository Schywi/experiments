package session

// The CPU spike of a cold Chromium happens between "the sandbox is bound" and "the
// browser is Ready". No pod cgroup exists before the first and the session sampler
// only starts after the second, so this file fills that window: it resolves the pod
// as soon as it exists and reads its cgroup until Ready, recording the peak.

import (
	"context"
	"runtime"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/logging"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/store"
)

// bootPeakCores turns a cpu.stat delta into cores for one sample window, clamped
// to the node's core count. A single pod cannot use more cores than the machine
// has: run B recorded a 15.6-core boot peak on a 6-core node, which is the sampler
// measuring its own interval rather than the browser.
func bootPeakCores(deltaUsec int64, elapsedSeconds float64, limit int) float64 {
	if deltaUsec <= 0 || elapsedSeconds <= 0 {
		return 0
	}
	cores := float64(deltaUsec) / 1e6 / elapsedSeconds
	if limit > 0 && cores > float64(limit) {
		return float64(limit)
	}
	return cores
}

// sampleBoot watches one instance while it boots. done is closed by the caller when
// the instance is Ready or the attempt failed.
func (r *Runner) sampleBoot(ctx context.Context, instance *sandbox.Instance, done <-chan struct{}) {
	if r.smp == nil {
		return
	}

	// The pod only exists once the sandbox is bound; wait for it without hammering
	// the API server (each check is one Get plus one List).
	resolve := time.NewTicker(200 * time.Millisecond)
	defer resolve.Stop()
	for {
		_, bootPodUID, _, _ := instance.Placement()
		if bootPodUID != "" {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-resolve.C:
			if r.ad.ResolvePod(ctx, instance) {
				break
			}
		}
	}

	// Boot is short: a warm adoption is ~250ms, so a one-second sampler sees
	// nothing at all. 100ms is the finest resolution cgroup reads allow without
	// measurable cost (two small files).
	interval := 100 * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var peakCores float64
	var lastCPU int64
	var lastAt time.Time
	node, podUID, _, _ := instance.Placement()
	if reading, ok, err := r.smp.Sample(ctx, podUID, node); err == nil && ok {
		lastCPU, lastAt = reading.CPUUsec, r.now()
	}
	for {
		select {
		case <-ctx.Done():
			r.recordBootCPU(instance, peakCores)
			return
		case <-done:
			r.recordBootCPU(instance, peakCores)
			return
		case <-ticker.C:
			node, podUID, _, _ := instance.Placement()
			reading, ok, err := r.smp.Sample(ctx, podUID, node)
			if err != nil || !ok {
				continue
			}
			now := r.now()
			if lastCPU > 0 && !lastAt.IsZero() {
				elapsed := now.Sub(lastAt).Seconds()
				cores := bootPeakCores(reading.CPUUsec-lastCPU, elapsed, runtime.NumCPU())
				if cores > peakCores {
					peakCores = cores
				}
			}
			lastCPU, lastAt = reading.CPUUsec, now
		}
	}
}

// recordBootCPU reports what the boot cost, per instance.
func (r *Runner) recordBootCPU(instance *sandbox.Instance, peakCores float64) {
	if peakCores <= 0 {
		return
	}
	instance.SetBootPeak(peakCores)
	if r.rec != nil {
		r.rec.BootCPU(peakCores)
	}
	// One line per instance: the spike size is the number the CPU study needs, and
	// there are tens of instances per run, not thousands of sessions.
	r.lg.Info("boot cpu", logging.F("instance", instance.ID),
		logging.F("peak_cores", peakCores))
}

// deriveBootConcurrency turns a measured boot spike into the number of browsers
// that may cold-start at once while the node stays under 80% of its cores:
// k_max = (cores * 0.8) / measured_peak, never below one. A zero or invalid
// input returns 0, which the caller reads as "nothing to derive".
func deriveBootConcurrency(cores, peakCores float64) int {
	if cores <= 0 || peakCores <= 0 {
		return 0
	}
	k := int((cores * 0.8) / peakCores)
	if k < 1 {
		k = 1
	}
	return k
}

// bootPeak is the worst measured boot spike for this run's class and packing,
// taken from this run's own instance rows and from prior runs in the same output
// directory. Only rows with the same class and contexts-per-browser count: a
// splash boot and an SPA boot are different costs, so mixing them would drift
// k_max toward whatever the last experiment happened to be.
func (r *Runner) bootPeak() (float64, error) {
	class := classKey(r.cfg.Class)
	peak, err := r.st.BootPeakCores(class, r.cfg.ContextsPerBrowser)
	if err != nil {
		return 0, err
	}
	prior, err := store.PriorBootPeakCores(r.cfg.OutDir, r.cfg.RunID, class, r.cfg.ContextsPerBrowser)
	if err != nil {
		return peak, err
	}
	if prior > peak {
		peak = prior
	}
	return peak, nil
}
