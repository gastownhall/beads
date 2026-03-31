package dolt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanStaleNomsLocks(t *testing.T) {
	t.Parallel()

	t.Run("removes stale LOCK file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		// Create a database directory with a stale noms LOCK
		nomsDir := filepath.Join(dir, "mydb", ".dolt", "noms")
		if err := os.MkdirAll(nomsDir, 0755); err != nil {
			t.Fatal(err)
		}
		lockPath := filepath.Join(nomsDir, "LOCK")
		if err := os.WriteFile(lockPath, []byte("stale"), 0600); err != nil {
			t.Fatal(err)
		}

		removed, errs := CleanStaleNomsLocks(dir)
		if len(errs) > 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if removed != 1 {
			t.Fatalf("expected 1 removed, got %d", removed)
		}

		// Verify file is gone
		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Fatal("LOCK file should have been removed")
		}
	})

	t.Run("handles multiple databases", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		// Create two databases with stale locks
		for _, db := range []string{"db1", "db2"} {
			nomsDir := filepath.Join(dir, db, ".dolt", "noms")
			if err := os.MkdirAll(nomsDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(nomsDir, "LOCK"), []byte("stale"), 0600); err != nil {
				t.Fatal(err)
			}
		}

		removed, errs := CleanStaleNomsLocks(dir)
		if len(errs) > 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if removed != 2 {
			t.Fatalf("expected 2 removed, got %d", removed)
		}
	})

	t.Run("no lock files present", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		// Create a database directory without a LOCK file
		nomsDir := filepath.Join(dir, "mydb", ".dolt", "noms")
		if err := os.MkdirAll(nomsDir, 0755); err != nil {
			t.Fatal(err)
		}

		removed, errs := CleanStaleNomsLocks(dir)
		if len(errs) > 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if removed != 0 {
			t.Fatalf("expected 0 removed, got %d", removed)
		}
	})

	t.Run("directory does not exist", func(t *testing.T) {
		t.Parallel()
		removed, errs := CleanStaleNomsLocks("/nonexistent/path/that/does/not/exist")
		if len(errs) > 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if removed != 0 {
			t.Fatalf("expected 0 removed, got %d", removed)
		}
	})

	t.Run("skips non-directory entries", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		// Create a regular file (not a directory) at the top level
		if err := os.WriteFile(filepath.Join(dir, "not-a-dir"), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}

		// Create a real database with a lock
		nomsDir := filepath.Join(dir, "realdb", ".dolt", "noms")
		if err := os.MkdirAll(nomsDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nomsDir, "LOCK"), []byte("stale"), 0600); err != nil {
			t.Fatal(err)
		}

		removed, errs := CleanStaleNomsLocks(dir)
		if len(errs) > 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if removed != 1 {
			t.Fatalf("expected 1 removed, got %d", removed)
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		removed, errs := CleanStaleNomsLocks(dir)
		if len(errs) > 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if removed != 0 {
			t.Fatalf("expected 0 removed, got %d", removed)
		}
	})

	t.Run("removes nested stats LOCK files", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		// Simulate the exact paths from the bug report:
		// .beads/dolt/lista/.dolt/stats/.dolt/noms/LOCK
		// .beads/dolt/.dolt/noms/LOCK
		// .beads/dolt/.dolt/stats/.dolt/noms/LOCK
		paths := []string{
			filepath.Join(dir, "lista", ".dolt", "stats", ".dolt", "noms"),
			filepath.Join(dir, ".dolt", "noms"),
			filepath.Join(dir, ".dolt", "stats", ".dolt", "noms"),
		}
		for _, p := range paths {
			if err := os.MkdirAll(p, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p, "LOCK"), []byte("stale"), 0600); err != nil {
				t.Fatal(err)
			}
		}

		// Also add a normal db-level lock
		normalNoms := filepath.Join(dir, "lista", ".dolt", "noms")
		if err := os.MkdirAll(normalNoms, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(normalNoms, "LOCK"), []byte("stale"), 0600); err != nil {
			t.Fatal(err)
		}

		removed, errs := CleanStaleNomsLocks(dir)
		if len(errs) > 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if removed != 4 {
			t.Fatalf("expected 4 removed, got %d", removed)
		}

		// Verify all are gone
		for _, p := range paths {
			lockPath := filepath.Join(p, "LOCK")
			if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
				t.Fatalf("LOCK file should have been removed: %s", lockPath)
			}
		}
		normalLock := filepath.Join(normalNoms, "LOCK")
		if _, err := os.Stat(normalLock); !os.IsNotExist(err) {
			t.Fatalf("LOCK file should have been removed: %s", normalLock)
		}
	})

	t.Run("ignores LOCK files not in noms directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		// A LOCK file in a non-noms directory should be ignored
		otherDir := filepath.Join(dir, "mydb", ".dolt", "other")
		if err := os.MkdirAll(otherDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(otherDir, "LOCK"), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}

		// A real noms LOCK should still be removed
		nomsDir := filepath.Join(dir, "mydb", ".dolt", "noms")
		if err := os.MkdirAll(nomsDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nomsDir, "LOCK"), []byte("stale"), 0600); err != nil {
			t.Fatal(err)
		}

		removed, errs := CleanStaleNomsLocks(dir)
		if len(errs) > 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if removed != 1 {
			t.Fatalf("expected 1 removed, got %d", removed)
		}

		// The non-noms LOCK should still exist
		otherLock := filepath.Join(otherDir, "LOCK")
		if _, err := os.Stat(otherLock); err != nil {
			t.Fatalf("non-noms LOCK file should not have been removed: %s", otherLock)
		}
	})
}
