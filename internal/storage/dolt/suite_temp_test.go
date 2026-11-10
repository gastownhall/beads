package dolt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// Guard the actual TestMain wiring: both APIs must allocate fixtures under
// the owned root or a failed fixture cleanup can leave a green, leaking suite.
func TestTempDirLandsUnderSuiteSweepRoot(t *testing.T) {
	root := filepath.Dir(os.Getenv(testCircuitBreakerDirEnv))
	if root == "" || root == "." {
		t.Fatal("TestMain did not pin its suite temp root")
	}
	if dir := t.TempDir(); !testutil.PathUnderSuiteRoot(dir, root) {
		t.Fatalf("t.TempDir() %q is outside suite root %q", dir, root)
	}
	dir, err := os.MkdirTemp("", "guard-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if !testutil.PathUnderSuiteRoot(dir, root) {
		t.Fatalf("os.MkdirTemp() %q is outside suite root %q", dir, root)
	}
}
