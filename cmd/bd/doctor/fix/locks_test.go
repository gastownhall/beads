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

	t.Run("stale dolt-access.lock removed at redirect target", func(t *testing.T) {
		tmpDir := t.TempDir()
		targetBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
		if err := os.MkdirAll(targetBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		worktreeRoot := filepath.Join(tmpDir, "worktree")
		worktreeBeadsDir := filepath.Join(worktreeRoot, ".beads")
		if err := os.MkdirAll(worktreeBeadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		redirectPath := filepath.Join(worktreeBeadsDir, "redirect")
		if err := os.WriteFile(redirectPath, []byte("../rig/.beads\n"), 0600); err != nil {
			t.Fatal(err)
		}

		lockPath := filepath.Join(targetBeadsDir, "dolt-access.lock")
		if err := os.WriteFile(lockPath, []byte("lock"), 0600); err != nil {
			t.Fatal(err)
		}
		oldTime := time.Now().Add(-10 * time.Minute)
		if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}

		if err := StaleLockFiles(worktreeRoot); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Error("stale dolt-access.lock at redirect target should be removed")
		}
		if _, err := os.Stat(redirectPath); err != nil {
			t.Errorf("redirect file should be preserved: %v", err)
		}
	})

	// noms LOCK tests removed: we no longer delete Dolt-internal noms/LOCK files.
	// Removing them causes unrecoverable data corruption.

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
