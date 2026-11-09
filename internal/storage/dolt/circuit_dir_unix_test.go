//go:build unix

package dolt

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// privateCircuitTempRoot points os.TempDir() at a fresh test-owned directory
// and clears the suite's circuit-directory override, so the production
// resolver picks the live directory. Every path it can produce stays under
// the returned root.
func privateCircuitTempRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(testCircuitBreakerDirEnv, "")
	t.Setenv("TMPDIR", root)
	return root
}

// GH#4223: state lives in a directory private to the effective uid, so one
// user's 0600 files can never shadow or block another user's breaker.
func TestCircuitBreakerDir_PrivatePerUser(t *testing.T) {
	root := privateCircuitTempRoot(t)
	wantDir := filepath.Join(root, fmt.Sprintf("beads-circuit-%d", os.Geteuid()))

	cb := maybeNewCircuitBreaker("127.0.0.1", 44444, "per_user")
	if cb == nil {
		t.Fatal("maybeNewCircuitBreaker returned nil for a private temp root")
	}
	recordDialFailure(cb, syscall.ECONNREFUSED)

	if got := filepath.Dir(cb.filePath); got != wantDir {
		t.Fatalf("breaker state dir = %q, want %q", got, wantDir)
	}
	dirInfo, err := os.Lstat(wantDir)
	if err != nil {
		t.Fatalf("stat live dir: %v", err)
	}
	if !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("live dir mode = %v, want a 0700 directory", dirInfo.Mode())
	}
	fileInfo, err := os.Stat(cb.filePath)
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %v, want 0600", fileInfo.Mode().Perm())
	}
	if _, err := os.Lstat(filepath.Join(root, "beads-circuit")); !os.IsNotExist(err) {
		t.Errorf("shared beads-circuit directory was created (stat err %v)", err)
	}
}

// The per-user directory keeps the breaker's purpose for one user: a process
// that trips it makes every other process of that user fail fast through the
// persisted state until the cooldown elapses.
func TestCircuitBreaker_SameUserInstancesSharePersistedState(t *testing.T) {
	privateCircuitTempRoot(t)
	t.Setenv("BEADS_TEST_MODE", "") // need real breaker behavior

	tripper := maybeNewCircuitBreaker("127.0.0.1", 44445, "shared_db")
	reader := maybeNewCircuitBreaker("127.0.0.1", 44445, "shared_db")
	if tripper == nil || reader == nil {
		t.Fatal("maybeNewCircuitBreaker returned nil for a private temp root")
	}
	if tripper == reader || tripper.filePath != reader.filePath {
		t.Fatalf("want two instances on one state file, got %q and %q", tripper.filePath, reader.filePath)
	}
	if !reader.Allow() {
		t.Fatal("a fresh breaker should allow")
	}
	for i := 0; i < circuitFailureThreshold; i++ {
		tripper.RecordFailure()
	}
	if reader.Allow() {
		t.Fatal("the other instance allowed a request during the cooldown of a tripped breaker")
	}
	if got := reader.State(); got != circuitOpen {
		t.Errorf("other instance state = %q, want %q", got, circuitOpen)
	}
}
