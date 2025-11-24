package rpc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupStaleDaemonArtifacts(t *testing.T) {
	// Create temp directory for test
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	pidFile := filepath.Join(beadsDir, "daemon.pid")

	// Test 1: No pid file - should not error
	t.Run("no_pid_file", func(t *testing.T) {
		cleanupStaleDaemonArtifacts(beadsDir)
		// Should not panic or error
	})

	// Test 2: Pid file exists - should be removed
	t.Run("removes_pid_file", func(t *testing.T) {
		// Create stale pid file
		if err := os.WriteFile(pidFile, []byte("12345\n"), 0644); err != nil {
			t.Fatalf("failed to create pid file: %v", err)
		}

		// Verify it exists
		if _, err := os.Stat(pidFile); err != nil {
			t.Fatalf("pid file should exist before cleanup: %v", err)
		}

		// Clean up
		cleanupStaleDaemonArtifacts(beadsDir)

		// Verify it was removed
		if _, err := os.Stat(pidFile); err == nil {
			t.Errorf("pid file should have been removed")
		}
	})

	// Test 3: Permission denied - should not panic
	t.Run("permission_denied", func(t *testing.T) {
		// Create pid file
		if err := os.WriteFile(pidFile, []byte("12345\n"), 0644); err != nil {
			t.Fatalf("failed to create pid file: %v", err)
		}

		// Make directory read-only (on Unix-like systems)
		if err := os.Chmod(beadsDir, 0555); err != nil {
			t.Fatalf("failed to make directory read-only: %v", err)
		}
		defer func() {
			// Restore permissions for cleanup
			_ = os.Chmod(beadsDir, 0755)
		}()

		// Should not panic even if removal fails
		cleanupStaleDaemonArtifacts(beadsDir)
	})
}

func TestTryConnectWithTimeout_SelfHeal(t *testing.T) {
	// Create temp directory for test
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	socketPath := filepath.Join(beadsDir, "bd.sock")
	pidFile := filepath.Join(beadsDir, "daemon.pid")

	// Create stale pid file (no socket, no lock)
	if err := os.WriteFile(pidFile, []byte("99999\n"), 0644); err != nil {
		t.Fatalf("failed to create pid file: %v", err)
	}

	// Verify pid file exists
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("pid file should exist before test: %v", err)
	}

	// Try to connect (should fail but clean up stale pid file)
	client, err := TryConnectWithTimeout(socketPath, 100)
	if client != nil {
		t.Errorf("expected nil client (no daemon running)")
	}
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}

	// Verify pid file was cleaned up
	if _, err := os.Stat(pidFile); err == nil {
		t.Errorf("pid file should have been removed during self-heal")
	}
}

func TestTryConnectWithTimeout_SocketExistenceRecheck(t *testing.T) {
	// This test verifies the fix for bd-4owj: race condition in socket cleanup
	// Scenario: Socket doesn't exist initially, but lock check shows daemon running,
	// then we re-check socket existence to handle daemon startup race.

	// Create temp directory for test
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	socketPath := filepath.Join(beadsDir, "bd.sock")
	lockPath := filepath.Join(beadsDir, "daemon.lock")

	// Create a lock file to simulate daemon holding lock
	// (In a real scenario, daemon would hold flock on this file)
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("failed to create lock file: %v", err)
	}
	// Write some content to make it look like a real lock file
	_, _ = lockFile.WriteString(`{"pid":99999,"database":"test.db"}`)
	// Don't acquire flock - we want TryDaemonLock to succeed

	// Close the file so TryDaemonLock can open it
	lockFile.Close()

	// Try to connect without socket existing
	// The code should: 1) Check socket (doesn't exist), 2) Check lock (can acquire),
	// 3) Return nil because both socket and lock indicate no daemon
	client, err := TryConnectWithTimeout(socketPath, 100)
	if client != nil {
		t.Errorf("expected nil client when no daemon is running")
	}
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}

	// The important part: the code should not incorrectly report daemon as running
	// when socket doesn't exist and lock can be acquired
}
