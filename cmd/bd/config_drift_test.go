package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/git"
)

func chdirForDriftTest(t *testing.T, dir string) {
	t.Helper()
	t.Chdir(dir)
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
}

// TestCheckHooksDriftNotGitRepo verifies hooks check skips when not in a git repo.
func TestCheckHooksDriftNotGitRepo(t *testing.T) {
	chdirForDriftTest(t, t.TempDir())

	items := checkHooksDrift()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Status != driftStatusSkipped {
		t.Errorf("expected status %q, got %q", driftStatusSkipped, items[0].Status)
	}
	if items[0].Check != "hooks" {
		t.Errorf("expected check %q, got %q", "hooks", items[0].Check)
	}
}

// TestCheckServerDriftNoBeadsDir verifies server check skips when no .beads exists.
func TestCheckServerDriftNoBeadsDir(t *testing.T) {
	chdirForDriftTest(t, t.TempDir())

	items := checkServerDrift()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Status != driftStatusSkipped {
		t.Errorf("expected status %q, got %q", driftStatusSkipped, items[0].Status)
	}
}

// TestCheckRemoteDriftNoBeadsDir verifies remote check skips when no .beads exists.
func TestCheckRemoteDriftNoBeadsDir(t *testing.T) {
	chdirForDriftTest(t, t.TempDir())

	items := checkRemoteDrift()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Status != driftStatusSkipped {
		t.Errorf("expected status %q, got %q", driftStatusSkipped, items[0].Status)
	}
}

// TestIsServerProbablyRunningNoPIDFile verifies false when no PID file exists.
func TestIsServerProbablyRunningNoPIDFile(t *testing.T) {
	tmpDir := t.TempDir()
	if isServerProbablyRunning(tmpDir) {
		t.Error("expected false with no PID file")
	}
}

// TestIsServerProbablyRunningBadPID verifies false for invalid PID content.
func TestIsServerProbablyRunningBadPID(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "dolt-server.pid")
	if err := os.WriteFile(pidFile, []byte("notanumber\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if isServerProbablyRunning(tmpDir) {
		t.Error("expected false with invalid PID")
	}
}

// TestIsServerProbablyRunningDeadPID verifies false for a non-existent process.
func TestIsServerProbablyRunningDeadPID(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "dolt-server.pid")
	// Use a very high PID that's unlikely to exist
	if err := os.WriteFile(pidFile, []byte("4999999\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if isServerProbablyRunning(tmpDir) {
		t.Error("expected false with dead PID")
	}
}

// TestIsServerProbablyRunningOwnPID verifies true for our own PID.
func TestIsServerProbablyRunningOwnPID(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "dolt-server.pid")
	pid := os.Getpid()
	if err := os.WriteFile(pidFile, []byte(filepath.Join(tmpDir, "")+"\n"), 0600); err != nil {
		// Write our own PID
	}
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", pid)), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !isServerProbablyRunning(tmpDir) {
		t.Error("expected true for own PID")
	}
}

func setupServerDriftTest(t *testing.T, sharedServer bool) (beadsDir, sharedDir string) {
	t.Helper()
	root := t.TempDir()
	beadsDir = filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create project beads dir: %v", err)
	}
	configBody := fmt.Sprintf("dolt:\n  shared-server: %t\n", sharedServer)
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configBody), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	sharedDir = filepath.Join(root, "shared-server")
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
	initConfigForTest(t)
	return beadsDir, sharedDir
}

func writeLiveServerPID(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create server state dir: %v", err)
	}
	pidFile := filepath.Join(dir, doltserver.PIDFileName)
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatalf("write live PID file: %v", err)
	}
}

func requireServerPIDLivenessForDriftTest(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("signal-0 PID liveness is not supported on Windows; covered by #4808")
	}
}

func TestCheckServerDriftSharedServerHealthyWithoutProjectPID(t *testing.T) {
	requireServerPIDLivenessForDriftTest(t)
	beadsDir, sharedDir := setupServerDriftTest(t, true)
	writeLiveServerPID(t, sharedDir)

	items := checkServerDrift()
	if len(items) != 1 || items[0].Status != driftStatusOK {
		t.Fatalf("checkServerDrift = %+v, want one ok item", items)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, doltserver.PIDFileName)); !os.IsNotExist(err) {
		t.Fatalf("project PID file unexpectedly exists: %v", err)
	}
}

func TestCheckServerDriftSharedServerEnvOverride(t *testing.T) {
	requireServerPIDLivenessForDriftTest(t)
	beadsDir, sharedDir := setupServerDriftTest(t, false)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	writeLiveServerPID(t, sharedDir)

	items := checkServerDrift()
	if len(items) != 1 || items[0].Status != driftStatusOK {
		t.Fatalf("checkServerDrift = %+v, want one ok item", items)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, doltserver.PIDFileName)); !os.IsNotExist(err) {
		t.Fatalf("project PID file unexpectedly exists: %v", err)
	}
}

func TestCheckServerDriftSharedServerMissingIgnoresProjectPID(t *testing.T) {
	requireServerPIDLivenessForDriftTest(t)
	beadsDir, sharedDir := setupServerDriftTest(t, true)
	writeLiveServerPID(t, beadsDir)

	items := checkServerDrift()
	if len(items) != 1 || items[0].Status != driftStatusDrift {
		t.Fatalf("checkServerDrift = %+v, want one drift item", items)
	}
	if _, err := os.Stat(sharedDir); !os.IsNotExist(err) {
		t.Fatalf("read-only drift check created or touched shared dir %q: %v", sharedDir, err)
	}
}

func TestCheckServerDriftStandaloneRunningPreservesInfo(t *testing.T) {
	requireServerPIDLivenessForDriftTest(t)
	beadsDir, _ := setupServerDriftTest(t, false)
	writeLiveServerPID(t, beadsDir)

	items := checkServerDrift()
	if len(items) != 1 || items[0].Status != driftStatusInfo {
		t.Fatalf("checkServerDrift = %+v, want one info item", items)
	}
}

// TestDriftItemStatuses verifies the status constants.
func TestDriftItemStatuses(t *testing.T) {
	if driftStatusOK != "ok" {
		t.Errorf("driftStatusOK = %q", driftStatusOK)
	}
	if driftStatusDrift != "drift" {
		t.Errorf("driftStatusDrift = %q", driftStatusDrift)
	}
	if driftStatusInfo != "info" {
		t.Errorf("driftStatusInfo = %q", driftStatusInfo)
	}
	if driftStatusSkipped != "skipped" {
		t.Errorf("driftStatusSkipped = %q", driftStatusSkipped)
	}
}

// TestRunDriftChecksReturnsResults verifies the aggregator returns results from all checks.
func TestRunDriftChecksReturnsResults(t *testing.T) {
	// When run from a non-beads directory, we should still get results (skipped checks)
	chdirForDriftTest(t, t.TempDir())

	items := runDriftChecks()
	if len(items) == 0 {
		t.Fatal("expected at least some drift items")
	}

	// All items should have a check name and status
	for _, item := range items {
		if item.Check == "" {
			t.Error("drift item has empty check name")
		}
		if item.Status == "" {
			t.Error("drift item has empty status")
		}
		if item.Message == "" {
			t.Error("drift item has empty message")
		}
	}
}
