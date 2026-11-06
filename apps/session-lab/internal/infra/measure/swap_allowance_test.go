package measure

import (
	"os"
	"path/filepath"
	"testing"
)

// writePodCgroup builds a pod slice with one scope per container, which is the
// layout kubelet actually produces: the slice carries "max" and every container
// scope carries the number that applies.
func writePodCgroup(t *testing.T, podDir string, scopes map[string]string) {
	t.Helper()
	if err := os.MkdirAll(podDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", podDir, err)
	}
	if err := os.WriteFile(filepath.Join(podDir, "memory.swap.max"), []byte("max\n"), 0o644); err != nil {
		t.Fatalf("write slice allowance: %v", err)
	}
	for name, value := range scopes {
		dir := filepath.Join(podDir, name+".scope")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "memory.swap.max"), []byte(value+"\n"), 0o644); err != nil {
			t.Fatalf("write %s allowance: %v", dir, err)
		}
	}
}

// TestSwapAllowancePrefersContainerScopes is the bug this function exists for: a
// pod slice says "max" while the browser's own container scope says 0, and
// reporting "max" would claim an allowance the browser does not have.
func TestSwapAllowancePrefersContainerScopes(t *testing.T) {
	podDir := filepath.Join(t.TempDir(), "pod.slice")
	// A stray scope left at "max" plus two real containers at 0: the live
	// layout measured on a browser pod.
	writePodCgroup(t, podDir, map[string]string{
		"cri-containerd-browser":     "0",
		"cri-containerd-forward":     "0",
		"cri-containerd-mostly-idle": "max",
	})

	got, ok := swapAllowance(podDir)
	if !ok {
		t.Fatal("swapAllowance reported no reading")
	}
	if got != 0 {
		t.Fatalf("allowance = %d, want 0 (the container scopes, not the slice)", got)
	}
}

// TestSwapAllowanceReadsTheNumberOfBytes checks the normal case: a writer set a
// finite allowance on the container.
func TestSwapAllowanceReadsTheNumberOfBytes(t *testing.T) {
	podDir := filepath.Join(t.TempDir(), "pod.slice")
	writePodCgroup(t, podDir, map[string]string{"cri-containerd-browser": "67108864"})

	got, ok := swapAllowance(podDir)
	if !ok || got != 67108864 {
		t.Fatalf("allowance = %d (ok=%v), want 67108864", got, ok)
	}
}

// TestSwapAllowanceAllMaxIsUnconstrained keeps "max" meaningful: every level says
// max only when nothing constrains the pod.
func TestSwapAllowanceAllMaxIsUnconstrained(t *testing.T) {
	podDir := filepath.Join(t.TempDir(), "pod.slice")
	writePodCgroup(t, podDir, map[string]string{"cri-containerd-browser": "max"})

	got, ok := swapAllowance(podDir)
	if !ok || got != -1 {
		t.Fatalf("allowance = %d (ok=%v), want -1 for max", got, ok)
	}
}
