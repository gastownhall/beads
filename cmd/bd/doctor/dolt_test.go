//go:build cgo

package doctor

import (
	"os"
	"path/filepath"
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

func TestDoltConn_CloseReleasesLock(t *testing.T) {
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

	// Create a doltConn with nil db (we're only testing lock release)
	// We can't create a real db without a Dolt database, but we can test
	// that Close() handles the lock release path.
	// Note: calling closeDoltDB with nil would panic, so we test lock
	// behavior separately.
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
	if len(checks) != 1 {
		t.Fatalf("expected 1 check for non-dolt backend, got %d", len(checks))
	}
	if checks[0].Status != StatusOK {
		t.Errorf("expected StatusOK for non-dolt backend, got %s", checks[0].Status)
	}
	if checks[0].Name != "Dolt Connection" {
		t.Errorf("expected check name 'Dolt Connection', got %q", checks[0].Name)
	}
	if checks[0].Message != "N/A (SQLite backend)" {
		t.Errorf("expected 'N/A (SQLite backend)' message, got %q", checks[0].Message)
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
