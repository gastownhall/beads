package daemonrunner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDaemonLockBasics(t *testing.T) {
	// Skip on Windows - file locking prevents reading lock file while locked
	if runtime.GOOS == "windows" {
		t.Skip("Windows file locking prevents reading locked files")
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")

	// Acquire lock
	lock, err := acquireDaemonLock(tmpDir, dbPath, "0.19.0")
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}
	defer lock.Close()

	// Verify lock file was created
	lockPath := filepath.Join(tmpDir, "daemon.lock")
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("Lock file was not created")
	}

	// Verify PID file was created
	pidPath := filepath.Join(tmpDir, "daemon.pid")
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		t.Error("PID file was not created")
	}

	// Read and verify lock metadata
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("Failed to read lock file: %v", err)
	}

	var info DaemonLockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("Failed to parse lock file: %v", err)
	}

	if info.PID != os.Getpid() {
		t.Errorf("Expected PID %d, got %d", os.Getpid(), info.PID)
	}
	if info.Database != dbPath {
		t.Errorf("Expected database %s, got %s", dbPath, info.Database)
	}
	if info.Version != "0.19.0" {
		t.Errorf("Expected version 0.19.0, got %s", info.Version)
	}
	if info.StartedAt.IsZero() {
		t.Error("Expected non-zero StartedAt timestamp")
	}
}

func TestDaemonLockExclusive(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")

	// Acquire first lock
	lock1, err := acquireDaemonLock(tmpDir, dbPath, "0.19.0")
	if err != nil {
		t.Fatalf("Failed to acquire first lock: %v", err)
	}
	defer lock1.Close()

	// Try to acquire second lock (should fail)
	lock2, err := acquireDaemonLock(tmpDir, dbPath, "0.19.0")
	if err != ErrDaemonLocked {
		if lock2 != nil {
			lock2.Close()
		}
		t.Errorf("Expected ErrDaemonLocked, got %v", err)
	}
}

func TestDaemonLockRelease(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")

	// Acquire lock
	lock, err := acquireDaemonLock(tmpDir, dbPath, "0.19.0")
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// Release lock
	if err := lock.Close(); err != nil {
		t.Fatalf("Failed to release lock: %v", err)
	}

	// Should be able to acquire again after release
	lock2, err := acquireDaemonLock(tmpDir, dbPath, "0.19.0")
	if err != nil {
		t.Fatalf("Failed to acquire lock after release: %v", err)
	}
	defer lock2.Close()
}

func TestDaemonLockCloseIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")

	lock, err := acquireDaemonLock(tmpDir, dbPath, "0.19.0")
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// Close multiple times should not error
	if err := lock.Close(); err != nil {
		t.Errorf("First close failed: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Errorf("Second close failed: %v", err)
	}
}
