package user

import (
	"errors"
	"sort"
	"sync"
)

var (
	// ErrUnknownUser is returned for an id that was never registered.
	ErrUnknownUser = errors.New("user: unknown user")
	// ErrAgentQuota is returned when a user's concurrent-agent budget is full.
	ErrAgentQuota = errors.New("user: concurrent agent quota exhausted")
	// ErrSessionQuota is returned when a user's concurrent-session budget is full.
	ErrSessionQuota = errors.New("user: concurrent session quota exhausted")
	// ErrInvalidUser is returned for a registration the registry cannot store.
	ErrInvalidUser = errors.New("user: registration needs an id and a non-negative limit")
)

// Registry holds users and counts what each one currently holds. It is the only
// place in the app where a quota decision is made.
//
// The counters are in memory on purpose: E7 runs a single process for the length
// of a measurement, and a durable ledger would be the persistence design this
// study excludes.
type Registry struct {
	mu       sync.Mutex
	users    map[ID]User
	agents   map[ID]int
	sessions map[ID]int
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		users:    map[ID]User{},
		agents:   map[ID]int{},
		sessions: map[ID]int{},
	}
}

// Put registers or replaces a user. Omitted limits take DefaultLimits.
func (r *Registry) Put(u User) error {
	if u.ID == "" {
		return ErrInvalidUser
	}
	if u.Limits.ConcurrentAgents < 0 || u.Limits.ConcurrentSessions < 0 {
		return ErrInvalidUser
	}
	u.Limits = withDefaults(u.Limits)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[u.ID] = u
	return nil
}

// Get returns the registered user.
func (r *Registry) Get(id ID) (User, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	return u, ok
}

// ReserveAgent charges one concurrent-agent slot and returns the grant the run
// works under. The slot is released by ReleaseAgent, whatever the run's outcome.
func (r *Registry) ReserveAgent(id ID) (Grant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return Grant{}, ErrUnknownUser
	}
	if r.agents[id] >= u.Limits.ConcurrentAgents {
		return Grant{}, ErrAgentQuota
	}
	r.agents[id]++
	return u.Grant(), nil
}

// ReleaseAgent returns one concurrent-agent slot.
func (r *Registry) ReleaseAgent(id ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.agents[id] > 0 {
		r.agents[id]--
	}
}

// ReserveSession charges one concurrent-session slot.
func (r *Registry) ReserveSession(id ID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[id]
	if !ok {
		return ErrUnknownUser
	}
	if r.sessions[id] >= u.Limits.ConcurrentSessions {
		return ErrSessionQuota
	}
	r.sessions[id]++
	return nil
}

// ReleaseSession returns one concurrent-session slot.
func (r *Registry) ReleaseSession(id ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[id] > 0 {
		r.sessions[id]--
	}
}

// InUse reports what the user currently holds.
func (r *Registry) InUse(id ID) (agents, sessions int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agents[id], r.sessions[id]
}

// IDs lists registered users in sorted order.
func (r *Registry) IDs() []ID {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ID, 0, len(r.users))
	for id := range r.users {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func withDefaults(l Limits) Limits {
	d := DefaultLimits()
	if l.ConcurrentAgents == 0 {
		l.ConcurrentAgents = d.ConcurrentAgents
	}
	if l.ConcurrentSessions == 0 {
		l.ConcurrentSessions = d.ConcurrentSessions
	}
	if l.MaxOpsPerCall == 0 {
		l.MaxOpsPerCall = d.MaxOpsPerCall
	}
	if l.MaxInferencesPerRun == 0 {
		l.MaxInferencesPerRun = d.MaxInferencesPerRun
	}
	if l.MaxRunDuration <= 0 {
		l.MaxRunDuration = d.MaxRunDuration
	}
	return l
}
