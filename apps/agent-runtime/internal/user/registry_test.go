package user

import "testing"

func TestPutFillsDefaultLimits(t *testing.T) {
	r := NewRegistry()
	if err := r.Put(User{ID: "e7"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	u, ok := r.Get("e7")
	if !ok {
		t.Fatal("user was not stored")
	}
	if u.Limits != DefaultLimits() {
		t.Fatalf("limits = %+v, want %+v", u.Limits, DefaultLimits())
	}
}

func TestPutRejectsEmptyIDAndNegativeLimits(t *testing.T) {
	r := NewRegistry()
	if err := r.Put(User{}); err != ErrInvalidUser {
		t.Fatalf("empty id: err = %v, want ErrInvalidUser", err)
	}
	if err := r.Put(User{ID: "e7", Limits: Limits{ConcurrentAgents: -1}}); err != ErrInvalidUser {
		t.Fatalf("negative limit: err = %v, want ErrInvalidUser", err)
	}
}

func TestUnknownUserCannotReserve(t *testing.T) {
	r := NewRegistry()
	if _, err := r.ReserveAgent("nobody"); err != ErrUnknownUser {
		t.Fatalf("err = %v, want ErrUnknownUser", err)
	}
	if err := r.ReserveSession("nobody"); err != ErrUnknownUser {
		t.Fatalf("err = %v, want ErrUnknownUser", err)
	}
}

func TestAgentQuotaIsEnforcedAndReturned(t *testing.T) {
	r := NewRegistry()
	if err := r.Put(User{ID: "e7", Limits: Limits{ConcurrentAgents: 2}}); err != nil {
		t.Fatalf("put: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := r.ReserveAgent("e7"); err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
	}
	if _, err := r.ReserveAgent("e7"); err != ErrAgentQuota {
		t.Fatalf("third reserve: err = %v, want ErrAgentQuota", err)
	}
	r.ReleaseAgent("e7")
	if _, err := r.ReserveAgent("e7"); err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
	if agents, _ := r.InUse("e7"); agents != 2 {
		t.Fatalf("in use = %d, want 2", agents)
	}
}

func TestSessionQuotaIsSeparateFromAgentQuota(t *testing.T) {
	r := NewRegistry()
	if err := r.Put(User{ID: "e7", Limits: Limits{ConcurrentAgents: 5, ConcurrentSessions: 1}}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := r.ReserveSession("e7"); err != nil {
		t.Fatalf("first session: %v", err)
	}
	if err := r.ReserveSession("e7"); err != ErrSessionQuota {
		t.Fatalf("second session: err = %v, want ErrSessionQuota", err)
	}
	// The agent budget is untouched by session pressure.
	for i := 0; i < 5; i++ {
		if _, err := r.ReserveAgent("e7"); err != nil {
			t.Fatalf("agent %d: %v", i, err)
		}
	}
	r.ReleaseSession("e7")
	if err := r.ReserveSession("e7"); err != nil {
		t.Fatalf("session after release: %v", err)
	}
}

func TestGrantCopiesTheScope(t *testing.T) {
	u := User{
		ID:      "e7",
		Limits:  Limits{MaxOpsPerCall: 3, MaxInferencesPerRun: 4},
		Actions: []string{"click"},
		Domains: []string{"example.com"},
		Denied:  []string{"delete"},
	}
	g := u.Grant()
	if g.User != "e7" || g.MaxOpsPerCall != 3 || g.MaxInferences != 4 {
		t.Fatalf("grant = %+v", g)
	}
	if !g.Allows("click") {
		t.Fatal("click should be allowed")
	}
	if g.Allows("navigate") {
		t.Fatal("navigate should not be in the grant")
	}
	g.Actions[0] = "navigate"
	if u.Actions[0] != "click" {
		t.Fatal("grant shares the user's slice")
	}
}
