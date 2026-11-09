// Package slots hands out one browsing slot per session.
//
// A slot is (instance, index) — one browser context inside a Chromium. With one
// context per browser a slot is the whole instance, which is how the runner
// behaved before contexts existed; with eight, an instance serves eight sessions
// and the claim count stops tracking the session count.
package slots

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
)

// Health is what the pool knows about a browser's ability to answer on its CDP
// port. An instance starts Unknown: nothing has asked it yet.
type Health int

const (
	HealthUnknown Health = iota
	HealthHealthy
	HealthUnhealthy
)

func (h Health) String() string {
	switch h {
	case HealthHealthy:
		return "healthy"
	case HealthUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// Instance is one Chromium the pool can hand out slots from.
type Instance struct {
	Instance *sandbox.Instance
	Capacity int
	InUse    int
	// Health is what the last probe said, and CheckedNS is when it said it. A
	// browser that cannot answer must not receive work, whatever its Kubernetes
	// phase says.
	Health    Health
	Failures  int
	CheckedNS int64
}

// Idle reports whether nothing is running in this instance.
func (i *Instance) Idle() bool { return i.InUse == 0 }

// Lease is what a session holds: a slot on a specific instance.
type Lease struct {
	Instance *sandbox.Instance
	// Index is which slot of that instance this lease occupies.
	Index int
	// Shared is true when the instance carries more than one slot, so
	// per-session resource readings are a division of the pod's usage rather
	// than an observation of this session alone.
	Shared bool
}

// Provider acquires and readies a new instance. It is called when no slot is
// free, which is the only path from the demand plane into the capacity plane.
type Provider func(ctx context.Context) (*sandbox.Instance, error)

// Checker asks a browser whether it can still serve work. It is called before a
// slot is handed out, at most once per health interval per instance.
type Checker func(ctx context.Context, instance *sandbox.Instance) error

// Options configures a pool. New covers the case that needs none of this.
type Options struct {
	PerInstance int
	Provide     Provider
	// Check is the health probe. Nil disables probing, which is the behaviour of
	// a pool built with New.
	Check Checker
	// HealthInterval is how long a probe result stays valid. Zero means every
	// assignment probes when Check is set.
	HealthInterval time.Duration
	// MaxProvisioning bounds instance creations in flight. One is what every
	// run before this option did, and the provider waits for readiness inside
	// that call, so the bound is a bound on cold boots at once. A larger value
	// lets the fleet grow k pods per boot instead of one, at the cost of k
	// concurrent CPU spikes on the node.
	MaxProvisioning int
	// Now is the clock, for tests.
	Now func() time.Time
}

// Pool allocates slots, creating instances on demand. It has no ceiling: the
// platform's refusal is the ceiling, and it is recorded when it happens.
type Pool struct {
	mu          sync.Mutex
	perInstance int
	provide     Provider
	check       Checker
	interval    time.Duration
	now         func() time.Time
	entries     []*Instance
	inUse       int
	// provisioning counts instance creations in flight, and wait is closed and
	// replaced whenever one finishes, so a burst of sessions does not each
	// create an instance of its own. max is how many may be in flight at once;
	// one means the fleet grows one cold path at a time.
	provisioning int
	max          int
	wait         chan struct{}
}

// New builds a pool with no health probe.
func New(perInstance int, provide Provider) *Pool {
	return NewWithOptions(Options{PerInstance: perInstance, Provide: provide})
}

// NewWithOptions builds a pool. perInstance below one is treated as one, so a
// zero-value configuration still means one session per instance.
func NewWithOptions(opts Options) *Pool {
	perInstance := opts.PerInstance
	if perInstance < 1 {
		perInstance = 1
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	maxProvisioning := opts.MaxProvisioning
	if maxProvisioning < 1 {
		maxProvisioning = 1
	}
	return &Pool{
		perInstance: perInstance, provide: opts.Provide, check: opts.Check,
		interval: opts.HealthInterval, now: now,
		max:  maxProvisioning,
		wait: make(chan struct{}),
	}
}

// Take blocks until this session has a slot, creating an instance if none is
// free. It never refuses: waiting is the only outcome when capacity is short,
// and the caller's context decides when to stop waiting. An instance whose
// health is unknown or stale is probed before its slot is handed out; one that
// fails the probe is skipped, and a second failure removes it.
func (p *Pool) Take(ctx context.Context) (Lease, error) {
	for {
		p.mu.Lock()
		if lease, ok := p.takeFreeLocked(); ok {
			p.mu.Unlock()
			return lease, nil
		}
		if candidate, ok := p.probeCandidateLocked(); ok {
			p.mu.Unlock()
			err := p.check(ctx, candidate.Instance)
			p.mu.Lock()
			p.recordProbeLocked(candidate, err)
			p.mu.Unlock()
			if ctx.Err() != nil {
				return Lease{}, ctx.Err()
			}
			continue
		}
		if p.provisioning >= p.max {
			// The permitted number of creations is already in flight: wait and
			// look again, because their free slots may be enough.
			wait := p.wait
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return Lease{}, ctx.Err()
			case <-wait:
				continue
			}
		}
		p.provisioning++
		p.mu.Unlock()

		instance, err := p.provide(ctx)

		p.mu.Lock()
		p.provisioning--
		p.notifyLocked()
		if err != nil {
			p.mu.Unlock()
			return Lease{}, err
		}
		// The new entry is Unknown: the loop probes it before it serves work.
		p.entries = append(p.entries, &Instance{Instance: instance, Capacity: p.perInstance})
		p.mu.Unlock()

		if ctx.Err() != nil {
			return Lease{}, ctx.Err()
		}
	}
}

// Give returns a slot to the pool. It reports whether that instance is now idle.
func (p *Pool) Give(lease Lease) (idle bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, entry := range p.entries {
		if entry.Instance == lease.Instance || entry.Instance.ID == lease.Instance.ID {
			if entry.InUse > 0 {
				entry.InUse--
			}
			if p.inUse > 0 {
				p.inUse--
			}
			return entry.Idle()
		}
	}
	return false
}

// RetireAndGive removes an instance and settles the slot it was holding in one
// lock. It is the failure path: a browser that could not answer on its CDP port
// must leave the pool, or the next Take hands its free slots to the next session
// and that session fails the same way. Safe to call twice, and safe on an
// instance the pool no longer holds.
func (p *Pool) RetireAndGive(lease Lease) (idle bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if lease.Instance == nil {
		return false
	}
	for i, entry := range p.entries {
		if entry.Instance.ID != lease.Instance.ID {
			continue
		}
		p.inUse -= entry.InUse
		if p.inUse < 0 {
			p.inUse = 0
		}
		p.entries = append(p.entries[:i], p.entries[i+1:]...)
		p.notifyLocked()
		return true
	}
	// Already retired: the lease is stale, and the counters were settled by the
	// call that removed the entry.
	return false
}

// Retire removes an instance from the pool. Used by the destroy policy, and by
// the runner when an instance is unusable.
func (p *Pool) Retire(instance *sandbox.Instance) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, entry := range p.entries {
		if entry.Instance.ID == instance.ID {
			p.inUse -= entry.InUse
			if p.inUse < 0 {
				p.inUse = 0
			}
			p.entries = append(p.entries[:i], p.entries[i+1:]...)
			return
		}
	}
}

// Snapshot describes the pool: instances, slots and slots in use, plus the health
// split, which is what says whether the pool can still serve work.
type Snapshot struct {
	Instances     int
	Slots         int
	InUse         int
	IdleInstances int
	Healthy       int
	Unhealthy     int
	Unknown       int
}

// Snapshot reads the current shape.
func (p *Pool) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	snap := Snapshot{Instances: len(p.entries), InUse: p.inUse}
	for _, entry := range p.entries {
		snap.Slots += entry.Capacity
		if entry.Idle() {
			snap.IdleInstances++
		}
		switch entry.Health {
		case HealthHealthy:
			snap.Healthy++
		case HealthUnhealthy:
			snap.Unhealthy++
		default:
			snap.Unknown++
		}
	}
	return snap
}

// InUseOn is how many sessions this instance is carrying, which is the divisor
// for a shared instance's resource readings.
func (p *Pool) InUseOn(instance *sandbox.Instance) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, entry := range p.entries {
		if entry.Instance.ID == instance.ID {
			return entry.InUse
		}
	}
	return 0
}

// Instances lists the instances the pool knows about.
func (p *Pool) Instances() []*Instance {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*Instance, len(p.entries))
	copy(out, p.entries)
	return out
}

// notifyLocked wakes everyone waiting for a creation to finish.
func (p *Pool) notifyLocked() {
	close(p.wait)
	p.wait = make(chan struct{})
}

// Capacity reports the configured slots per instance.
func (p *Pool) Capacity() int { return p.perInstance }

// MaxProvisioning is how many instance creations may be in flight at once.
func (p *Pool) MaxProvisioning() int { return p.max }

// takeFreeLocked finds a slot, preferring an instance that already has sessions
// so idle instances are drained to zero before new ones are filled. An entry
// whose health is unknown or stale is not eligible: it has to be probed first.
func (p *Pool) takeFreeLocked() (Lease, bool) {
	chosen := p.pickLocked()
	if chosen == nil {
		return Lease{}, false
	}
	index := chosen.InUse
	chosen.InUse++
	p.inUse++
	return Lease{Instance: chosen.Instance, Index: index, Shared: p.perInstance > 1}, true
}

// pickLocked returns the instance a slot should come from, or nil. Only entries
// that are free, not unhealthy, and (when a checker exists) freshly probed are
// candidates.
func (p *Pool) pickLocked() *Instance {
	var chosen *Instance
	for _, entry := range p.entries {
		if entry.InUse >= entry.Capacity || entry.Health == HealthUnhealthy {
			continue
		}
		if p.check != nil && !p.freshLocked(entry) {
			continue
		}
		if chosen == nil || entry.InUse > chosen.InUse {
			chosen = entry
		}
	}
	return chosen
}

// probeCandidateLocked returns the instance that has to be probed before it can
// serve work, if any.
func (p *Pool) probeCandidateLocked() (*Instance, bool) {
	if p.check == nil {
		return nil, false
	}
	var candidate *Instance
	for _, entry := range p.entries {
		if entry.InUse >= entry.Capacity || entry.Health == HealthUnhealthy {
			continue
		}
		if p.freshLocked(entry) {
			continue
		}
		if candidate == nil || entry.InUse > candidate.InUse {
			candidate = entry
		}
	}
	return candidate, candidate != nil
}

// freshLocked reports whether a healthy probe result is still inside the
// interval. With no interval, every assignment probes.
func (p *Pool) freshLocked(entry *Instance) bool {
	if entry.Health != HealthHealthy {
		return false
	}
	if p.interval <= 0 {
		return false
	}
	return p.now().UnixNano()-entry.CheckedNS < int64(p.interval)
}

// recordProbeLocked remembers the outcome of a probe. One failure takes the
// instance out of assignment; a second removes it, because a browser that has
// failed twice is not coming back on its own.
func (p *Pool) recordProbeLocked(entry *Instance, err error) {
	entry.CheckedNS = p.now().UnixNano()
	if err == nil {
		entry.Health = HealthHealthy
		entry.Failures = 0
		return
	}
	entry.Failures++
	entry.Health = HealthUnhealthy
	if entry.Failures < 2 {
		return
	}
	for i, candidate := range p.entries {
		if candidate != entry {
			continue
		}
		p.inUse -= entry.InUse
		if p.inUse < 0 {
			p.inUse = 0
		}
		p.entries = append(p.entries[:i], p.entries[i+1:]...)
		p.notifyLocked()
		return
	}
}

// String describes the pool for a log line.
func (p *Pool) String() string {
	snap := p.Snapshot()
	return fmt.Sprintf("slots: %d instances x %d = %d slots, %d in use",
		snap.Instances, p.perInstance, snap.Slots, snap.InUse)
}
