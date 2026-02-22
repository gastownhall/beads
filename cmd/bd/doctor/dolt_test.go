package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/lockfile"
)

// TestRunDoltHealthChecks_NonDoltBackend was removed: SQLite backend no longer
// exists. GetBackend() always returns "dolt" after the dolt-native cleanup.
// (bd-yqpwy)

func TestRunDoltHealthChecks_DoltBackendNoServer(t *testing.T) {
	// Server-only mode: without a running dolt sql-server, the connection
	// check should return StatusError.
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

	// Point at a port nothing listens on to ensure connection fails
	t.Setenv("BEADS_DOLT_SERVER_PORT", "59998")

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) < 1 {
		t.Fatalf("expected at least 1 check, got %d", len(checks))
	}

	if checks[0].Name != "Dolt Connection" {
		t.Errorf("expected first check to be 'Dolt Connection', got %q", checks[0].Name)
	}
	if checks[0].Status != StatusError {
		t.Errorf("expected StatusError (no server running), got %s: %s", checks[0].Status, checks[0].Message)
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

// TestLockContention was removed: server-only mode does not acquire advisory
// locks — the server handles its own locking. Lock contention is no longer
// a doctor concern for connection establishment.

func TestServerMode_NoLockAcquired(t *testing.T) {
	// Server-only mode never acquires advisory locks.
	// We force a non-listening port so the connection always fails.
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

	t.Setenv("BEADS_DOLT_SERVER_PORT", "59999")

	checks := RunDoltHealthChecks(tmpDir)
	if len(checks) < 1 {
		t.Fatalf("expected at least 1 check (connection error + lock health), got %d", len(checks))
	}

	check := checks[0]

	// Should fail with a connection error, NOT a lock error
	if check.Status != StatusError {
		t.Errorf("expected StatusError (server unreachable), got %s", check.Status)
	}

	// The error should NOT be about lock acquisition
	if strings.Contains(check.Detail, "access lock") {
		t.Errorf("should not attempt lock acquisition, but Detail contains %q: %s",
			"access lock", check.Detail)
	}

	// Verify no lock file was created
	lockPath := filepath.Join(beadsDir, "dolt-access.lock")
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("lock file should not exist, but found at %s", lockPath)
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
