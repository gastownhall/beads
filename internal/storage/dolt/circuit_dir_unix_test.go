//go:build unix

package dolt

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// redirectTempRootForCircuitTest points the production circuit breaker
// directory policy at a private per-test temp root: TMPDIR/TEMP/TMP drive
// os.TempDir() on every platform, and the test override is cleared so the
// production location rules run. Every directory the tests touch stays
// inside the returned root.
func redirectTempRootForCircuitTest(t *testing.T) string {
	t.Helper()
	t.Setenv(testCircuitBreakerDirEnv, "")
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	return root
}

// perUserCircuitDir returns the per-user circuit state directory the Unix
// location policy must resolve to under root, computed independently of
// circuitBreakerDir so a regression to the unsuffixed shared name fails
// here rather than mirroring the production helper (GH#4223).
func perUserCircuitDir(root string) string {
	return filepath.Join(root, "beads-circuit-"+strconv.Itoa(os.Geteuid()))
}

// TestCircuitBreakerDir_PrivatePerUser pins the per-user location policy
// (GH#4223): on a multi-user host one shared temp-root directory meant one
// user's 0600 state files were unreadable and unwritable by every other
// user, silently discarding their breaker state. Live state must land in a
// uid-suffixed directory created 0700, and the unsuffixed shared directory
// must not be created at all.
func TestCircuitBreakerDir_PrivatePerUser(t *testing.T) {
	root := redirectTempRootForCircuitTest(t)

	cb := maybeNewCircuitBreaker("127.0.0.1", 44444, "peruser")
	if cb == nil {
		t.Fatal("breaker construction over a fresh private temp root must not be disabled")
	}
	recordDialFailure(cb, dialError(syscall.ECONNREFUSED))

	wantDir := perUserCircuitDir(root)
	if got := filepath.Dir(cb.filePath); got != wantDir {
		t.Errorf("state directory = %s, want per-user %s", got, wantDir)
	}
	dirInfo, err := os.Stat(wantDir)
	if err != nil {
		t.Fatalf("per-user state directory must exist: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("per-user state directory mode = %o, want 0700", got)
	}
	fileInfo, err := os.Stat(cb.filePath)
	if err != nil {
		t.Fatalf("recorded failure must persist state: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("state file mode = %o, want 0600", got)
	}
	if _, err := os.Lstat(filepath.Join(root, "beads-circuit")); !os.IsNotExist(err) {
		t.Errorf("unsuffixed shared directory must not be created (stat err %v)", err)
	}
}

// TestCircuitBreaker_SameUserInstancesShareState verifies the coordination
// the per-user directory preserves: two independently constructed breakers
// for one endpoint and database under one user's temp root share persisted
// state, so a tripped breaker makes the other instance fail fast without
// waiting out the cooldown. No sleep is needed: the state is inspected
// immediately after the threshold with fresh timestamps.
func TestCircuitBreaker_SameUserInstancesShareState(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	root := redirectTempRootForCircuitTest(t)

	cb1 := maybeNewCircuitBreaker("127.0.0.1", 44444, "coord")
	cb2 := maybeNewCircuitBreaker("127.0.0.1", 44444, "coord")
	if cb1 == nil || cb2 == nil {
		t.Fatal("breaker construction over a fresh private temp root must not be disabled")
	}
	if cb1.filePath != cb2.filePath {
		t.Errorf("same endpoint and database must share one state file: %s vs %s", cb1.filePath, cb2.filePath)
	}
	if want := perUserCircuitDir(root); filepath.Dir(cb1.filePath) != want {
		t.Errorf("shared state must live in the per-user directory %s, got %s", want, filepath.Dir(cb1.filePath))
	}

	for i := 0; i < circuitFailureThreshold; i++ {
		cb1.RecordFailure()
	}
	if got := cb2.State(); got != circuitOpen {
		t.Fatalf("second instance state = %q, want %q from persisted state", got, circuitOpen)
	}
	if cb2.Allow() {
		t.Error("second instance must reject during the cooldown via the persisted state")
	}
}
