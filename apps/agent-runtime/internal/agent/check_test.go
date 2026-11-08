package agent

import "testing"

func testSnapshot() Snapshot {
	return Snapshot{
		Version:     13,
		URL:         "http://example.com/",
		Fingerprint: "http://example.com/|1000",
		Nodes: []Node{
			{Ref: Ref{Version: 13, ID: 1}, Role: "button", Name: "Sign in", Box: Box{X: 10, Y: 10, W: 40, H: 20}},
			{Ref: Ref{Version: 13, ID: 2}, Role: "button", Name: "Delete card", Box: Box{X: 60, Y: 10, W: 40, H: 20}},
			{Ref: Ref{Version: 13, ID: 3}, Role: "button", Name: "Hidden", Box: Box{}, Disabled: true},
		},
	}
}

func testPolicy() Policy {
	return Policy{
		Actions:       Actions(),
		Domains:       []string{"example.com"},
		Denied:        []string{"delete"},
		MaxOpsPerCall: 3,
	}
}

func TestCheckAllowsAGrantedClick(t *testing.T) {
	ops := []Op{{Action: ActionClick, Ref: Ref{Version: 13, ID: 1}}}
	allowed, rejects := Check(testPolicy(), testSnapshot(), ops)
	if len(rejects) != 0 {
		t.Fatalf("rejects = %+v, want none", rejects)
	}
	if len(allowed) != 1 {
		t.Fatalf("allowed = %d, want 1", len(allowed))
	}
}

func TestCheckRejectsAStaleRef(t *testing.T) {
	ops := []Op{{Action: ActionClick, Ref: Ref{Version: 12, ID: 1}}}
	allowed, rejects := Check(testPolicy(), testSnapshot(), ops)
	if len(allowed) != 0 {
		t.Fatalf("allowed = %d, want 0", len(allowed))
	}
	if len(rejects) != 1 || rejects[0].Code != RejectStaleRef {
		t.Fatalf("rejects = %+v, want one %s", rejects, RejectStaleRef)
	}
}

func TestCheckRejectsAnUnknownRef(t *testing.T) {
	ops := []Op{{Action: ActionClick, Ref: Ref{Version: 13, ID: 9}}}
	_, rejects := Check(testPolicy(), testSnapshot(), ops)
	if len(rejects) != 1 || rejects[0].Code != RejectUnknownRef {
		t.Fatalf("rejects = %+v, want one %s", rejects, RejectUnknownRef)
	}
}

func TestCheckRejectsADeniedTarget(t *testing.T) {
	ops := []Op{{Action: ActionClick, Ref: Ref{Version: 13, ID: 2}}}
	_, rejects := Check(testPolicy(), testSnapshot(), ops)
	if len(rejects) != 1 || rejects[0].Code != RejectTargetDenied {
		t.Fatalf("rejects = %+v, want one %s", rejects, RejectTargetDenied)
	}
}

func TestCheckRejectsAnUnactionableNode(t *testing.T) {
	ops := []Op{{Action: ActionClick, Ref: Ref{Version: 13, ID: 3}}}
	_, rejects := Check(testPolicy(), testSnapshot(), ops)
	if len(rejects) != 1 || rejects[0].Code != RejectNotActionable {
		t.Fatalf("rejects = %+v, want one %s", rejects, RejectNotActionable)
	}
}

func TestCheckStopsAtTheFirstReject(t *testing.T) {
	ops := []Op{
		{Action: ActionClick, Ref: Ref{Version: 13, ID: 1}},
		{Action: ActionClick, Ref: Ref{Version: 12, ID: 1}},
		{Action: ActionClick, Ref: Ref{Version: 13, ID: 1}},
	}
	allowed, rejects := Check(testPolicy(), testSnapshot(), ops)
	if len(allowed) != 1 {
		t.Fatalf("allowed = %d, want 1 (the ops before the reject)", len(allowed))
	}
	if len(rejects) != 1 || rejects[0].Index != 1 || rejects[0].Code != RejectStaleRef {
		t.Fatalf("rejects = %+v, want one stale-ref at index 1", rejects)
	}
}

func TestCheckRejectsABatchOverTheLimit(t *testing.T) {
	snap := testSnapshot()
	ops := make([]Op, 0, 5)
	for i := 0; i < 5; i++ {
		ops = append(ops, Op{Action: ActionClick, Ref: Ref{Version: 13, ID: 1}})
	}
	allowed, rejects := Check(testPolicy(), snap, ops)
	if len(allowed) != 3 {
		t.Fatalf("allowed = %d, want 3", len(allowed))
	}
	if len(rejects) != 1 || rejects[0].Code != RejectBatchTooLarge || rejects[0].Index != 3 {
		t.Fatalf("rejects = %+v, want batch-too-large at index 3", rejects)
	}
}

func TestCheckEnforcesTheDomainGrant(t *testing.T) {
	policy := testPolicy()
	policy.Domains = []string{"example.com"}

	ok, rejects := Check(policy, testSnapshot(), []Op{{Action: ActionNavigate, URL: "http://example.com/next"}})
	if len(rejects) != 0 || len(ok) != 1 {
		t.Fatalf("granted navigate: allowed=%d rejects=%+v", len(ok), rejects)
	}

	_, rejects = Check(policy, testSnapshot(), []Op{{Action: ActionNavigate, URL: "http://elsewhere.test/"}})
	if len(rejects) != 1 || rejects[0].Code != RejectDomainNotGranted {
		t.Fatalf("rejects = %+v, want %s", rejects, RejectDomainNotGranted)
	}

	policy.Domains = nil
	_, rejects = Check(policy, testSnapshot(), []Op{{Action: ActionNavigate, URL: "http://example.com/next"}})
	if len(rejects) != 1 || rejects[0].Code != RejectDomainNotGranted {
		t.Fatalf("no domains: rejects = %+v, want %s", rejects, RejectDomainNotGranted)
	}
}

func TestCheckRejectsAnUngrantedAction(t *testing.T) {
	policy := testPolicy()
	policy.Actions = []string{ActionClick}
	_, rejects := Check(policy, testSnapshot(), []Op{{Action: ActionScroll, DY: 600}})
	if len(rejects) != 1 || rejects[0].Code != RejectActionNotGranted {
		t.Fatalf("rejects = %+v, want %s", rejects, RejectActionNotGranted)
	}
}

func TestCheckRejectsMalformedOps(t *testing.T) {
	cases := map[string]Op{
		"navigate without url": {Action: ActionNavigate},
		"click without ref":    {Action: ActionClick},
		"type without text":    {Action: ActionType, Ref: Ref{Version: 13, ID: 1}},
	}
	for name, op := range cases {
		_, rejects := Check(testPolicy(), testSnapshot(), []Op{op})
		if len(rejects) != 1 || rejects[0].Code != RejectBadOp {
			t.Fatalf("%s: rejects = %+v, want %s", name, rejects, RejectBadOp)
		}
	}
}

func TestCheckRejectsAnActionOutsideTheVocabulary(t *testing.T) {
	// An action the grant names but the tools do not implement is a bad op; an
	// action the grant omits is refused earlier, as not granted.
	policy := testPolicy()
	policy.Actions = append(policy.Actions, "delete")
	_, rejects := Check(policy, testSnapshot(), []Op{{Action: "delete", Ref: Ref{Version: 13, ID: 1}}})
	if len(rejects) != 1 || rejects[0].Code != RejectBadOp {
		t.Fatalf("rejects = %+v, want %s", rejects, RejectBadOp)
	}
}

func TestParseRefRoundTrip(t *testing.T) {
	ref, err := ParseRef("13:5")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ref.Version != 13 || ref.ID != 5 {
		t.Fatalf("ref = %+v", ref)
	}
	if ref.String() != "13:5" {
		t.Fatalf("string = %q", ref.String())
	}
	for _, bad := range []string{"", "13", "a:b", "13:", ":5"} {
		if _, err := ParseRef(bad); err == nil {
			t.Fatalf("%q parsed without error", bad)
		}
	}
}

func TestSnapshotLookups(t *testing.T) {
	snap := testSnapshot()
	if node, ok := snap.Node(Ref{Version: 13, ID: 1}); !ok || node.Name != "Sign in" {
		t.Fatalf("node = %+v ok=%v", node, ok)
	}
	if _, ok := snap.Node(Ref{Version: 13, ID: 7}); ok {
		t.Fatal("id 7 should not exist")
	}
	if matches := snap.Named("DELETE"); len(matches) != 1 {
		t.Fatalf("named(delete) = %d, want 1 (case-insensitive)", len(matches))
	}
	if _, unique := ExactlyOne(snap, "sign in"); !unique {
		t.Fatal("Sign in should resolve uniquely")
	}
	if _, unique := ExactlyOne(snap, "button sign"); unique {
		t.Fatal("a needle matching two nodes must not resolve")
	}
}
