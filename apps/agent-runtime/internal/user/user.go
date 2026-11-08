// Package user owns identity, quota and permission scope for one unit of work.
//
// It knows nothing about browsers, sessions, objectives or models: everything it
// hands out is a plain struct of strings and integers. That is what lets it stay
// independent of package agent, which owns the work itself. The two are joined
// only in cmd/, and a test in cmd/ fails the build if that stops being true.
package user

import "time"

// ID identifies an owner.
type ID string

// Limits bounds what one user may hold at once.
//
// Zero values are replaced by DefaultLimits when the user is registered, so an
// omitted field means "one", never "unlimited".
type Limits struct {
	ConcurrentAgents    int
	ConcurrentSessions  int
	MaxOpsPerCall       int
	MaxInferencesPerRun int
	MaxRunDuration      time.Duration
}

// DefaultLimits is the smallest sane allocation: one agent, one session, a short
// run. A deployment that wants more says so explicitly.
func DefaultLimits() Limits {
	return Limits{
		ConcurrentAgents:    1,
		ConcurrentSessions:  1,
		MaxOpsPerCall:       5,
		MaxInferencesPerRun: 8,
		MaxRunDuration:      2 * time.Minute,
	}
}

// User is the ownership and authorization boundary.
type User struct {
	ID       ID
	Limits   Limits
	Actions  []string // what a run acting for this user may do
	Domains  []string // where a run acting for this user may navigate
	Denied   []string // substrings of an accessible name a run must never act on
}

// Grant is the capability set one run receives. It carries no objective and no
// model choice; those belong to the agent definition.
type Grant struct {
	User           ID
	Actions        []string
	Domains        []string
	Denied         []string
	MaxOpsPerCall  int
	MaxInferences  int
	MaxRunDuration time.Duration
}

// Grant projects the user's scope into the shape a run needs.
func (u User) Grant() Grant {
	return Grant{
		User:           u.ID,
		Actions:        append([]string(nil), u.Actions...),
		Domains:        append([]string(nil), u.Domains...),
		Denied:         append([]string(nil), u.Denied...),
		MaxOpsPerCall:  u.Limits.MaxOpsPerCall,
		MaxInferences:  u.Limits.MaxInferencesPerRun,
		MaxRunDuration: u.Limits.MaxRunDuration,
	}
}

// Allows reports whether the action is inside the grant.
func (g Grant) Allows(action string) bool {
	for _, a := range g.Actions {
		if a == action {
			return true
		}
	}
	return false
}
