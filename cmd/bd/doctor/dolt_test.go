//go:build cgo

package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
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

// TestRunDoltHealthChecks_NonDoltBackend was removed: SQLite backend no longer
// exists. GetBackend() always returns "dolt" after the dolt-native cleanup.
// (bd-yqpwy)

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
	if len(checks) < 1 {
		t.Fatalf("expected at least 1 check, got %d", len(checks))
	}

	// The embedded Dolt driver auto-initializes an empty directory into a
	// working database, so connection succeeds even without a pre-existing DB.
	// Verify the first check is the connection check and it returns OK.
	if checks[0].Name != "Dolt Connection" {
		t.Errorf("expected first check to be 'Dolt Connection', got %q", checks[0].Name)
	}
	if checks[0].Status != StatusOK {
		t.Errorf("expected StatusOK for auto-initialized dolt DB, got %s: %s", checks[0].Status, checks[0].Message)
	}

	// Verify lock file is cleaned up after checks
	exLock, err := dolt.AcquireAccessLock(doltDir, true, 1*time.Second)
	if err != nil {
		t.Errorf("could not acquire exclusive lock after RunDoltHealthChecks: %v", err)
	} else {
		exLock.Release()
	}
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
	if len(checks) < 1 {
		t.Fatalf("expected at least 1 check on lock contention, got %d", len(checks))
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

	// Fix suggestion mentions lock files
	if !strings.Contains(check.Fix, "lock") {
		t.Errorf("expected Fix to contain %q, got %q", "lock", check.Fix)
	}
}

func TestServerMode_NoLockAcquired(t *testing.T) {
	// Verifies that server mode skips lock acquisition entirely.
	// BEADS_DOLT_SERVER_MODE=1 triggers server mode, which attempts MySQL
	// connection instead of embedded Dolt. We force a non-listening port
	// so the connection always fails, regardless of whether a real Dolt
	// server is running on the machine.
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

	// Enable server mode via env var, pointing at a port nothing listens on
	t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "59999")

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) < 1 {
		t.Fatalf("expected at least 1 check (connection error + lock health), got %d", len(checks))
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

func TestCheckLockHealth_NoIssues(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configContent := []byte(`{"backend":"dolt"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatal(err)
	}

	check := CheckLockHealth(tmpDir)
	if check.Status != StatusOK {
		t.Errorf("expected OK status, got %s: %s", check.Status, check.Message)
	}
}

func TestCheckLockHealth_UnlockedNomsLOCK(t *testing.T) {
	// A noms LOCK file that is NOT flock'd should not produce a warning.
	// This is the common case: a previous bd process created the file,
	// released the flock on close, but the file persists on disk.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	nomsDir := filepath.Join(beadsDir, "dolt", "beads", ".dolt", "noms")
	if err := os.MkdirAll(nomsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configContent := []byte(`{"backend":"dolt"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(nomsDir, "LOCK")
	if err := os.WriteFile(lockPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckLockHealth(tmpDir)
	if check.Status != StatusOK {
		t.Errorf("expected OK status with unlocked noms LOCK file, got %s (detail: %s)", check.Status, check.Detail)
	}
}

func TestCheckLockHealth_HeldNomsLOCK(t *testing.T) {
	// A noms LOCK file that IS flock'd should produce a warning —
	// another process is actively using the database.
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	nomsDir := filepath.Join(beadsDir, "dolt", "beads", ".dolt", "noms")
	if err := os.MkdirAll(nomsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configContent := []byte(`{"backend":"dolt"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(nomsDir, "LOCK")
	if err := os.WriteFile(lockPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	// Hold an exclusive flock on the LOCK file to simulate an active dolt process
	f, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
		t.Fatalf("failed to acquire flock: %v", err)
	}
	defer func() { _ = lockfile.FlockUnlock(f) }()

	check := CheckLockHealth(tmpDir)
	if check.Status != StatusWarning {
		t.Errorf("expected Warning status with held noms LOCK, got %s", check.Status)
	}
	if !strings.Contains(check.Detail, "noms LOCK") {
		t.Errorf("expected detail to mention noms LOCK, got: %s", check.Detail)
	}
}

func TestCheckLockHealth_DetectsHeldAdvisoryLock(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configContent := []byte(`{"backend":"dolt"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatal(err)
	}

	// Hold the advisory lock
	lock, err := dolt.AcquireAccessLock(doltDir, true, 2*time.Second)
	if err != nil {
		t.Fatalf("failed to acquire advisory lock: %v", err)
	}
	defer lock.Release()

	check := CheckLockHealth(tmpDir)
	if check.Status != StatusWarning {
		t.Errorf("expected Warning status with advisory lock held, got %s", check.Status)
	}
	if !strings.Contains(check.Detail, "advisory lock") {
		t.Errorf("expected detail to mention advisory lock, got: %s", check.Detail)
	}
}

func TestCheckLockHealth_NonDoltBackend(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write SQLite backend config
	configContent := []byte(`{"backend":"sqlite"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), configContent, 0o644); err != nil {
		t.Fatal(err)
	}

	check := CheckLockHealth(tmpDir)
	if check.Status != StatusOK {
		t.Errorf("expected OK for non-Dolt backend, got %s", check.Status)
	}
}
