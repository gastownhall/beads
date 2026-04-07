package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestCheckHooksDriftNotGitRepo verifies hooks check skips when not in a git repo.
func TestCheckHooksDriftNotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

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
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

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
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

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
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

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
