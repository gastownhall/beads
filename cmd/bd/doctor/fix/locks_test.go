package fix

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStaleLockFiles(t *testing.T) {
	t.Run("no beads dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := StaleLockFiles(tmpDir); err != nil {
			t.Errorf("expected no error for missing .beads dir, got %v", err)
		}
	})

	t.Run("no lock files", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := StaleLockFiles(tmpDir); err != nil {
			t.Errorf("expected no error for empty .beads dir, got %v", err)
		}
	})

	t.Run("stale bootstrap lock still removed", func(t *testing.T) {
		// Verify we didn't break existing cleanup
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		lockPath := filepath.Join(beadsDir, "dolt.bootstrap.lock")
		if err := os.WriteFile(lockPath, []byte("lock"), 0600); err != nil {
			t.Fatal(err)
		}
		oldTime := time.Now().Add(-10 * time.Minute)
		if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}

		if err := StaleLockFiles(tmpDir); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Error("stale bootstrap lock should be removed")
		}
	})
}
