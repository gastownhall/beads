//go:build cgo

package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

func TestAccessLock_FileCreated(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	// Acquire a shared lock directly using the dolt package
	lock, err := dolt.AcquireAccessLock(doltDir, false, 5*time.Second)
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}

	// Verify lock file exists at .beads/dolt-access.lock (parent of doltDir)
	lockPath := filepath.Join(beadsDir, "dolt-access.lock")
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("lock file not created at expected path")
	}

	lock.Release()
}

func TestAccessLock_ReleaseIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	lock, err := dolt.AcquireAccessLock(doltDir, false, 5*time.Second)
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}

	// Multiple releases should not panic
	lock.Release()
	lock.Release()
}

func TestAccessLock_ReleaseAllowsReacquisition(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	lock, err := dolt.AcquireAccessLock(doltDir, false, 5*time.Second)
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}

	lock.Release()

	// After release, another exclusive lock should succeed immediately
	exLock, err := dolt.AcquireAccessLock(doltDir, true, 1*time.Second)
	if err != nil {
		t.Fatalf("exclusive lock after release failed: %v", err)
	}
	exLock.Release()
}

func TestRunDoltHealthChecks_NonDoltBackend(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	// Explicitly set SQLite backend via metadata.json.
	// Without metadata.json, dolt.GetBackendFromConfig defaults to "dolt".
	configContent := []byte(`{"backend":"sqlite"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) != 4 {
		t.Fatalf("expected 4 checks for non-dolt backend, got %d", len(checks))
	}

	// Validate all 4 N/A checks
	expectedChecks := []struct {
		name     string
		category string
	}{
		{"Dolt Connection", CategoryCore},
		{"Dolt Schema", CategoryCore},
		{"Dolt-JSONL Sync", CategoryData},
		{"Dolt Status", CategoryData},
	}

	for i, expected := range expectedChecks {
		if checks[i].Status != StatusOK {
			t.Errorf("check[%d] %q: expected StatusOK, got %s", i, expected.name, checks[i].Status)
		}
		if checks[i].Name != expected.name {
			t.Errorf("check[%d]: expected name %q, got %q", i, expected.name, checks[i].Name)
		}
		if checks[i].Message != "N/A (SQLite backend)" {
			t.Errorf("check[%d] %q: expected 'N/A (SQLite backend)' message, got %q", i, expected.name, checks[i].Message)
		}
		if checks[i].Category != expected.category {
			t.Errorf("check[%d] %q: expected category %q, got %q", i, expected.name, expected.category, checks[i].Category)
		}
	}
}

func TestRunDoltHealthChecks_DoltBackendNoDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	// Write metadata.json marking this as dolt backend
	configContent := []byte(`{"backend":"dolt"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Create the dolt directory (needed for lock acquisition)
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}

	// With dolt backend but no actual database, expect an error
	if checks[0].Status != StatusError {
		t.Errorf("expected StatusError for dolt backend without DB, got %s", checks[0].Status)
	}

	// Verify lock file is cleaned up after error
	lockPath := filepath.Join(beadsDir, "dolt-access.lock")
	// Lock file may exist (flock semantics) but should be unlocked.
	// Test that we can acquire an exclusive lock, proving it was released.
	exLock, err := dolt.AcquireAccessLock(doltDir, true, 1*time.Second)
	if err != nil {
		t.Errorf("could not acquire exclusive lock after RunDoltHealthChecks error: %v", err)
	} else {
		exLock.Release()
	}
	_ = lockPath // used for documentation
}

func TestRunDoltHealthChecks_CheckNameAndCategory(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) == 0 {
		t.Fatal("expected at least 1 check")
	}

	check := checks[0]
	if check.Category != CategoryCore {
		t.Errorf("expected CategoryCore, got %q", check.Category)
	}
}

func TestLockContention(t *testing.T) {
	// Verifies that RunDoltHealthChecks returns a StatusError with proper
	// error messages when an exclusive lock is already held (simulating
	// another process). The 15s lock timeout in openDoltDBWithLock is
	// hardcoded, so this test takes ~15s to complete.
	if testing.Short() {
		t.Skip("skipping lock contention test in short mode (takes ~15s)")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	// Write metadata.json marking this as dolt backend
	configContent := []byte(`{"backend":"dolt"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Acquire an EXCLUSIVE lock to block shared acquisition by RunDoltHealthChecks.
	// openDoltDBWithLock tries shared lock with 15s timeout; it will fail because
	// exclusive lock is held.
	exLock, err := dolt.AcquireAccessLock(doltDir, true, 5*time.Second)
	if err != nil {
		t.Fatalf("failed to acquire exclusive lock: %v", err)
	}
	defer exLock.Release()

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) != 1 {
		t.Fatalf("expected 1 check on lock contention, got %d", len(checks))
	}

	check := checks[0]

	// Verify error status and check name
	if check.Status != StatusError {
		t.Errorf("expected StatusError, got %s", check.Status)
	}
	if check.Name != "Dolt Connection" {
		t.Errorf("expected check name %q, got %q", "Dolt Connection", check.Name)
	}

	// The error wraps as "acquire access lock: dolt access lock timeout ..."
	if !strings.Contains(check.Detail, "access lock") {
		t.Errorf("expected Detail to contain %q, got %q", "access lock", check.Detail)
	}

	// Fix suggestion mentions LOCK files
	if !strings.Contains(check.Fix, "LOCK") {
		t.Errorf("expected Fix to contain %q, got %q", "LOCK", check.Fix)
	}
}

func TestServerMode_NoLockAcquired(t *testing.T) {
	// Verifies that server mode skips lock acquisition entirely.
	// BEADS_DOLT_SERVER_MODE=1 triggers server mode, which attempts MySQL
	// connection instead of embedded Dolt. The MySQL connection will fail
	// (no server running), but the important assertions are:
	// 1. Error does NOT contain "access lock" (lock was skipped)
	// 2. No lock file was created
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	configContent := []byte(`{"backend":"dolt"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	// Enable server mode via env var
	t.Setenv("BEADS_DOLT_SERVER_MODE", "1")

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) != 1 {
		t.Fatalf("expected 1 check (connection error), got %d", len(checks))
	}

	check := checks[0]

	// Server mode should fail with a connection error, NOT a lock error
	if check.Status != StatusError {
		t.Errorf("expected StatusError (server unreachable), got %s", check.Status)
	}

	// The error should NOT be about lock acquisition — server mode skips locking
	if strings.Contains(check.Detail, "access lock") {
		t.Errorf("server mode should not attempt lock acquisition, but Detail contains %q: %s",
			"access lock", check.Detail)
	}

	// Verify no lock file was created at .beads/dolt-access.lock
	lockPath := filepath.Join(beadsDir, "dolt-access.lock")
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("lock file should not exist in server mode, but found at %s", lockPath)
	}
}
