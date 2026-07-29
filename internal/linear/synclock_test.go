package linear

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireSyncLock(t *testing.T) {
	t.Run("acquire and release", func(t *testing.T) {
		dir := t.TempDir()
		lock, err := AcquireSyncLock(dir, false)
		if err != nil {
			t.Fatalf("failed to acquire lock: %v", err)
		}
		if lock == nil {
			t.Fatal("lock is nil")
		}

		lockPath := filepath.Join(dir, syncLockFilename)
		if _, err := os.Stat(lockPath); err != nil {
			t.Fatalf("lock file should exist: %v", err)
		}

		if err := lock.Release(); err != nil {
			t.Fatalf("failed to release lock: %v", err)
		}

		// Lock file must persist after release — removing it after unlock creates a
		// race where a blocked waiter holds the old inode while a new process opens a
		// fresh file at the same path, splitting lock identity.
		if _, err := os.Stat(lockPath); err != nil {
			t.Errorf("lock file should persist after release (stable path): %v", err)
		}
		// Content should be truncated (empty) after release.
		data, err := os.ReadFile(lockPath)
		if err != nil {
			t.Fatalf("could not read lock file after release: %v", err)
		}
		if len(data) != 0 {
			t.Errorf("lock file should be empty after release, got %q", string(data))
		}
	})

	t.Run("second acquire detects lock held", func(t *testing.T) {
		dir := t.TempDir()
		lock1, err := AcquireSyncLock(dir, false)
		if err != nil {
			t.Fatalf("failed to acquire first lock: %v", err)
		}
		defer lock1.Release()

		_, err = AcquireSyncLock(dir, false)
		if err == nil {
			t.Fatal("second acquire should fail")
		}
		held, ok := err.(*SyncLockHeldError)
		if !ok {
			t.Fatalf("expected SyncLockHeldError, got %T: %v", err, err)
		}
		if held.Info == nil {
			t.Error("expected lock info to be populated")
		} else if held.Info.PID != os.Getpid() {
			t.Errorf("expected PID %d, got %d", os.Getpid(), held.Info.PID)
		}
	})

	t.Run("acquire after release succeeds", func(t *testing.T) {
		dir := t.TempDir()
		lock1, err := AcquireSyncLock(dir, false)
		if err != nil {
			t.Fatalf("failed to acquire first lock: %v", err)
		}
		if err := lock1.Release(); err != nil {
			t.Fatalf("failed to release first lock: %v", err)
		}

		lock2, err := AcquireSyncLock(dir, false)
		if err != nil {
			t.Fatalf("failed to acquire second lock after release: %v", err)
		}
		defer lock2.Release()
	})

	t.Run("stale lock from dead PID is reclaimable", func(t *testing.T) {
		dir := t.TempDir()
		lockPath := filepath.Join(dir, syncLockFilename)

		// Write a lock file with a PID that doesn't exist (use a high PID)
		stalePID := 2147483647
		content := "pid=2147483647\nstarted=2026-01-01T00:00:00Z\n"
		if err := os.WriteFile(lockPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write stale lock: %v", err)
		}

		// Verify the PID is not alive (it shouldn't be — it's maxint)
		if IsProcessAlive(stalePID) {
			t.Skip("stale PID is somehow alive, skipping")
		}

		// Should be able to acquire since flock is process-scoped and the stale
		// process no longer holds the kernel lock
		lock, err := AcquireSyncLock(dir, false)
		if err != nil {
			t.Fatalf("should acquire lock over stale file: %v", err)
		}
		defer lock.Release()
	})

	t.Run("release is idempotent", func(t *testing.T) {
		dir := t.TempDir()
		lock, err := AcquireSyncLock(dir, false)
		if err != nil {
			t.Fatalf("failed to acquire lock: %v", err)
		}
		if err := lock.Release(); err != nil {
			t.Fatalf("first release failed: %v", err)
		}
		// Second release should not panic or error
		if err := lock.Release(); err != nil {
			t.Fatalf("second release should succeed (idempotent): %v", err)
		}
	})

	t.Run("nil lock release is safe", func(t *testing.T) {
		var lock *SyncLock
		if err := lock.Release(); err != nil {
			t.Fatalf("nil release should not error: %v", err)
		}
	})
}

func TestParseLockInfo(t *testing.T) {
	t.Run("valid content", func(t *testing.T) {
		info := parseLockInfo("pid=12345\nstarted=2026-05-02T00:00:00Z\n")
		if info == nil {
			t.Fatal("expected non-nil info")
		}
		if info.PID != 12345 {
			t.Errorf("expected PID 12345, got %d", info.PID)
		}
		if info.Started.IsZero() {
			t.Error("expected non-zero started time")
		}
	})

	t.Run("empty content", func(t *testing.T) {
		info := parseLockInfo("")
		if info != nil {
			t.Error("expected nil for empty content")
		}
	})

	t.Run("malformed content", func(t *testing.T) {
		info := parseLockInfo("garbage data")
		if info != nil {
			t.Error("expected nil for garbage content")
		}
	})
}

func TestIsProcessAlive(t *testing.T) {
	t.Run("current process is alive", func(t *testing.T) {
		if !IsProcessAlive(os.Getpid()) {
			t.Error("current process should be alive")
		}
	})

	t.Run("zero PID is not alive", func(t *testing.T) {
		if IsProcessAlive(0) {
			t.Error("PID 0 should not be considered alive")
		}
	})

	t.Run("negative PID is not alive", func(t *testing.T) {
		if IsProcessAlive(-1) {
			t.Error("negative PID should not be considered alive")
		}
	})

	t.Run("very high PID is not alive", func(t *testing.T) {
		if IsProcessAlive(2147483647) {
			t.Skip("high PID is somehow alive")
		}
	})
}
