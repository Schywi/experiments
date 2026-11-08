package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestPackagesStayIndependent is the rule the study exists to keep: user owns
// identity and quota, agent owns the work, and neither is allowed to know about
// the other. Only this command package may import both.
func TestPackagesStayIndependent(t *testing.T) {
	cases := []struct{ pkg, forbidden string }{
		{"./internal/user", "internal/agent"},
		{"./internal/agent", "internal/user"},
		{"./internal/user", "internal/agent/tools"},
		{"./internal/user", "internal/agent/reasoners"},
	}
	for _, c := range cases {
		deps := listDeps(t, c.pkg)
		if strings.Contains(deps, c.forbidden) {
			t.Fatalf("%s depends on %s; the two packages are meant to stay independent", c.pkg, c.forbidden)
		}
	}
}

// TestCommandWiresBothPackages proves the split is real rather than vacuous: the
// wiring point has to import both, or the app could not run.
func TestCommandWiresBothPackages(t *testing.T) {
	deps := listDeps(t, "./cmd/agent-runtime")
	for _, want := range []string{"internal/user", "internal/agent"} {
		if !strings.Contains(deps, want) {
			t.Fatalf("cmd/agent-runtime does not import %s", want)
		}
	}
}

func listDeps(t *testing.T, pkg string) string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", pkg)
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	return string(out)
}
