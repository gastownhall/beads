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

	t.Run("fresh dolt-access.lock preserved", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		lockPath := filepath.Join(beadsDir, "dolt-access.lock")
		if err := os.WriteFile(lockPath, []byte("lock"), 0600); err != nil {
			t.Fatal(err)
		}

		if err := StaleLockFiles(tmpDir); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, err := os.Stat(lockPath); os.IsNotExist(err) {
			t.Error("fresh dolt-access.lock should NOT be removed")
		}
	})

	t.Run("stale dolt-access.lock removed", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		lockPath := filepath.Join(beadsDir, "dolt-access.lock")
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
			t.Error("stale dolt-access.lock should be removed")
		}
	})

	t.Run("noms LOCK always removed by doctor fix", func(t *testing.T) {
		tmpDir := t.TempDir()
		nomsDir := filepath.Join(tmpDir, ".beads", "dolt", "beads", ".dolt", "noms")
		if err := os.MkdirAll(nomsDir, 0755); err != nil {
			t.Fatal(err)
		}

		lockPath := filepath.Join(nomsDir, "LOCK")
		if err := os.WriteFile(lockPath, []byte("lock"), 0600); err != nil {
			t.Fatal(err)
		}

		// In server mode (the only mode now), noms LOCK files are always
		// removed by doctor --fix. The Dolt server manages its own locks;
		// if the user is running doctor --fix, stale locks are the likely
		// cause of the problem they're trying to fix.
		if err := StaleLockFiles(tmpDir); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Error("noms LOCK should be removed by doctor --fix")
		}
	})

	t.Run("stale noms LOCK removed", func(t *testing.T) {
		tmpDir := t.TempDir()
		nomsDir := filepath.Join(tmpDir, ".beads", "dolt", "beads", ".dolt", "noms")
		if err := os.MkdirAll(nomsDir, 0755); err != nil {
			t.Fatal(err)
		}

		lockPath := filepath.Join(nomsDir, "LOCK")
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
			t.Error("stale noms LOCK should be removed")
		}
	})

	t.Run("stale noms LOCK multi-database", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")

		var lockPaths []string
		for _, dbName := range []string{"beads", "other"} {
			nomsDir := filepath.Join(beadsDir, "dolt", dbName, ".dolt", "noms")
			if err := os.MkdirAll(nomsDir, 0755); err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(nomsDir, "LOCK")
			if err := os.WriteFile(lockPath, []byte("lock"), 0600); err != nil {
				t.Fatal(err)
			}
			oldTime := time.Now().Add(-10 * time.Minute)
			if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
				t.Fatal(err)
			}
			lockPaths = append(lockPaths, lockPath)
		}

		if err := StaleLockFiles(tmpDir); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, lockPath := range lockPaths {
			if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
				t.Errorf("stale noms LOCK should be removed: %s", lockPath)
			}
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
