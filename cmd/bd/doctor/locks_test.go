package doctor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckStaleLockFiles(t *testing.T) {
	t.Run("no beads dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		result := CheckStaleLockFiles(tmpDir)
		if result.Status != StatusOK {
			t.Errorf("expected OK for missing .beads dir, got %s: %s", result.Status, result.Message)
		}
	})

	t.Run("no lock files", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		result := CheckStaleLockFiles(tmpDir)
		if result.Status != StatusOK {
			t.Errorf("expected OK for no lock files, got %s: %s", result.Status, result.Message)
		}
	})

	t.Run("fresh bootstrap lock not stale", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create a fresh bootstrap lock (should not be flagged)
		lockPath := filepath.Join(beadsDir, "dolt.bootstrap.lock")
		if err := os.WriteFile(lockPath, []byte("lock"), 0600); err != nil {
			t.Fatal(err)
		}

		result := CheckStaleLockFiles(tmpDir)
		if result.Status != StatusOK {
			t.Errorf("expected OK for fresh lock file, got %s: %s", result.Status, result.Message)
		}
	})

	t.Run("stale bootstrap lock detected", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create an old bootstrap lock
		lockPath := filepath.Join(beadsDir, "dolt.bootstrap.lock")
		if err := os.WriteFile(lockPath, []byte("lock"), 0600); err != nil {
			t.Fatal(err)
		}
		// Set modification time to 10 minutes ago
		oldTime := time.Now().Add(-10 * time.Minute)
		if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}

		result := CheckStaleLockFiles(tmpDir)
		if result.Status != StatusWarning {
			t.Errorf("expected Warning for stale bootstrap lock, got %s: %s", result.Status, result.Message)
		}
	})

	t.Run("stale sync lock detected", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create an old sync lock
		lockPath := filepath.Join(beadsDir, ".sync.lock")
		if err := os.WriteFile(lockPath, []byte("lock"), 0600); err != nil {
			t.Fatal(err)
		}
		// Set modification time to 2 hours ago
		oldTime := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}

		result := CheckStaleLockFiles(tmpDir)
		if result.Status != StatusWarning {
			t.Errorf("expected Warning for stale sync lock, got %s: %s", result.Status, result.Message)
		}
	})

	t.Run("stale startlock detected", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create an old startlock
		lockPath := filepath.Join(beadsDir, "bd.sock.startlock")
		if err := os.WriteFile(lockPath, []byte("12345"), 0600); err != nil {
			t.Fatal(err)
		}
		// Set modification time to 2 minutes ago
		oldTime := time.Now().Add(-2 * time.Minute)
		if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}

		result := CheckStaleLockFiles(tmpDir)
		if result.Status != StatusWarning {
			t.Errorf("expected Warning for stale startlock, got %s: %s", result.Status, result.Message)
		}
	})

	t.Run("multiple stale locks", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		oldTime := time.Now().Add(-2 * time.Hour)

		// Create stale bootstrap lock
		bootstrapLock := filepath.Join(beadsDir, "dolt.bootstrap.lock")
		if err := os.WriteFile(bootstrapLock, []byte("lock"), 0600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(bootstrapLock, oldTime, oldTime)

		// Create stale sync lock
		syncLock := filepath.Join(beadsDir, ".sync.lock")
		if err := os.WriteFile(syncLock, []byte("lock"), 0600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(syncLock, oldTime, oldTime)

		result := CheckStaleLockFiles(tmpDir)
		if result.Status != StatusWarning {
			t.Errorf("expected Warning for multiple stale locks, got %s: %s", result.Status, result.Message)
		}
		// Should mention count
		if result.Message == "" {
			t.Error("expected non-empty message for stale locks")
		}
	})
}
