package agent

import (
	"net/url"
	"strings"
)

// Reject codes. They are part of the record: a run that ends with every op
// rejected is a different outcome from a run whose ops failed in the browser.
const (
	RejectActionNotGranted = "action-not-granted"
	RejectStaleRef         = "stale-ref"
	RejectUnknownRef       = "unknown-ref"
	RejectNotActionable    = "not-actionable"
	RejectDomainNotGranted = "domain-not-granted"
	RejectBatchTooLarge    = "batch-too-large"
	RejectTargetDenied     = "target-denied"
	RejectBadOp            = "bad-op"
)

// Policy is the deterministic permission set a run works under. It is built from
// a user's grant in cmd/ and never consults a model.
type Policy struct {
	Actions       []string
	Domains       []string // hosts a navigate may reach; empty means none
	Denied        []string // substrings of an accessible name that may not be acted on
	MaxOpsPerCall int
}

// Reject explains why an op was not dispatched.
type Reject struct {
	Index  int
	Code   string
	Detail string
}

// Check decides which ops may run. It stops at the first op that fails: the ops
// before it keep their place, the ops after it are never dispatched, and the
// caller re-inspects before trying again.
//
// At most one reject is returned per call, for the op that stopped the batch.
func Check(p Policy, snap Snapshot, ops []Op) ([]Op, []Reject) {
	limit := p.MaxOpsPerCall
	if limit <= 0 {
		limit = len(ops)
	}
	for i, op := range ops {
		if i >= limit {
			return ops[:i], []Reject{{Index: i, Code: RejectBatchTooLarge,
				Detail: "batch of " + itoa(len(ops)) + " exceeds " + itoa(limit)}}
		}
		if code, detail := checkOne(p, snap, op); code != "" {
			return ops[:i], []Reject{{Index: i, Code: code, Detail: detail}}
		}
	}
	return ops, nil
}

func checkOne(p Policy, snap Snapshot, op Op) (code, detail string) {
	if !allowed(p.Actions, op.Action) {
		return RejectActionNotGranted, op.Action
	}
	switch op.Action {
	case ActionNavigate:
		if op.URL == "" {
			return RejectBadOp, "navigate without a url"
		}
		u, err := url.Parse(op.URL)
		if err != nil || u.Host == "" {
			return RejectBadOp, "unparseable url " + op.URL
		}
		if len(p.Domains) == 0 {
			return RejectDomainNotGranted, "no domains granted"
		}
		if !allowed(p.Domains, u.Host) {
			return RejectDomainNotGranted, u.Host
		}
		return "", ""
	case ActionScroll:
		return "", ""
	case ActionClick, ActionType:
		if op.Ref.IsZero() {
			return RejectBadOp, op.Action + " without a ref"
		}
		if op.Ref.Version != snap.Version {
			return RejectStaleRef, op.Ref.String() + " against snapshot " + itoa(snap.Version)
		}
		node, ok := snap.Node(op.Ref)
		if !ok {
			return RejectUnknownRef, op.Ref.String()
		}
		if node.Disabled || node.Box.Empty() {
			return RejectNotActionable, node.Label()
		}
		if denied(p.Denied, node.Name) {
			return RejectTargetDenied, node.Label()
		}
		if op.Action == ActionType && op.Text == "" {
			return RejectBadOp, "type without text"
		}
		return "", ""
	}
	return RejectBadOp, "unknown action " + op.Action
}

func allowed(set []string, value string) bool {
	for _, s := range set {
		if s == value {
			return true
		}
	}
	return false
}

func denied(patterns []string, name string) bool {
	lower := strings.ToLower(name)
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" && strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
