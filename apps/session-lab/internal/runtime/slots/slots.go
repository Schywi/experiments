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

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
)

// Instance is one Chromium the pool can hand out slots from.
type Instance struct {
	Instance *sandbox.Instance
	Capacity int
	InUse    int
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

// Pool allocates slots, creating instances on demand. It has no ceiling: the
// platform's refusal is the ceiling, and it is recorded when it happens.
type Pool struct {
	mu          sync.Mutex
	perInstance int
	provide     Provider
	entries     []*Instance
	inUse       int
	// provisioning counts instance creations in flight, and wait is closed and
	// replaced whenever one finishes, so a burst of sessions does not each
	// create an instance of its own.
	provisioning int
	wait         chan struct{}
}

// New builds a pool. perInstance below one is treated as one, so a zero-value
// configuration still means one session per instance.
func New(perInstance int, provide Provider) *Pool {
	if perInstance < 1 {
		perInstance = 1
	}
	return &Pool{perInstance: perInstance, provide: provide, wait: make(chan struct{})}
}

// Take blocks until this session has a slot, creating an instance if none is
// free. It never refuses: waiting is the only outcome when capacity is short,
// and the caller's context decides when to stop waiting.
func (p *Pool) Take(ctx context.Context) (Lease, error) {
	for {
		p.mu.Lock()
		if lease, ok := p.takeFreeLocked(); ok {
			p.mu.Unlock()
			return lease, nil
		}
		if p.provisioning > 0 {
			// Someone else is already creating an instance: wait for it and
			// look again, because its free slots may be enough.
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
		entry := &Instance{Instance: instance, Capacity: p.perInstance}
		p.entries = append(p.entries, entry)
		entry.InUse++
		p.inUse++
		p.mu.Unlock()

		if ctx.Err() != nil {
			return Lease{}, ctx.Err()
		}
		return Lease{Instance: instance, Index: 0, Shared: p.perInstance > 1}, nil
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

// Snapshot describes the pool: instances, slots and slots in use.
type Snapshot struct {
	Instances     int
	Slots         int
	InUse         int
	IdleInstances int
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

// takeFreeLocked finds a slot, preferring an instance that already has sessions
// so idle instances are drained to zero before new ones are filled.
func (p *Pool) takeFreeLocked() (Lease, bool) {
	var chosen *Instance
	for _, entry := range p.entries {
		if entry.InUse >= entry.Capacity {
			continue
		}
		if chosen == nil || entry.InUse > chosen.InUse {
			chosen = entry
		}
	}
	if chosen == nil {
		return Lease{}, false
	}
	index := chosen.InUse
	chosen.InUse++
	p.inUse++
	return Lease{Instance: chosen.Instance, Index: index, Shared: p.perInstance > 1}, true
}

// String describes the pool for a log line.
func (p *Pool) String() string {
	snap := p.Snapshot()
	return fmt.Sprintf("slots: %d instances x %d = %d slots, %d in use",
		snap.Instances, p.perInstance, snap.Slots, snap.InUse)
}
