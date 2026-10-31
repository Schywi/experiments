package session

// The CPU spike of a cold Chromium happens between "the sandbox is bound" and "the
// browser is Ready". No pod cgroup exists before the first and the session sampler
// only starts after the second, so this file fills that window: it resolves the pod
// as soon as it exists and reads its cgroup until Ready, recording the peak.

import (
	"context"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/logging"
)

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
	for instance.PodUID == "" {
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
	if reading, ok, err := r.smp.Sample(ctx, instance.PodUID, instance.Node); err == nil && ok {
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
			reading, ok, err := r.smp.Sample(ctx, instance.PodUID, instance.Node)
			if err != nil || !ok {
				continue
			}
			now := r.now()
			if lastCPU > 0 && !lastAt.IsZero() {
				elapsed := now.Sub(lastAt).Seconds()
				if elapsed > 0 {
					cores := float64(reading.CPUUsec-lastCPU) / 1e6 / elapsed
					if cores > peakCores {
						peakCores = cores
					}
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
	instance.BootCPUPeakCores = peakCores
	if r.rec != nil {
		r.rec.BootCPU(peakCores)
	}
	// One line per instance: the spike size is the number the CPU study needs, and
	// there are tens of instances per run, not thousands of sessions.
	r.lg.Info("boot cpu", logging.F("instance", instance.ID),
		logging.F("peak_cores", peakCores))
}
