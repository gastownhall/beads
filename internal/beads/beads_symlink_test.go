//go:build integration
// +build integration

package beads

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindAllDatabases_SymlinkDeduplication verifies that FindAllDatabases
// properly deduplicates databases when symlinks are present in the path
func TestFindAllDatabases_SymlinkDeduplication(t *testing.T) {
	// Create a temporary directory structure
	tmpDir, err := os.MkdirTemp("", "beads-symlink-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Resolve symlinks (macOS /var -> /private/var, etc.)
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create structure:
	// tmpDir/
	//   real/
	//     .beads/test.db
	//   symlink_to_real -> real/
	//   subdir/
	//     (working directory here)

	// Create real directory with .beads database
	realDir := filepath.Join(tmpDir, "real")
	if err := os.MkdirAll(realDir, 0750); err != nil {
		t.Fatal(err)
	}

	beadsDir := filepath.Join(realDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	if err := os.WriteFile(dbPath, []byte("fake db"), 0600); err != nil {
		t.Fatal(err)
	}

	// Create symlink to real directory
	symlinkDir := filepath.Join(tmpDir, "symlink_to_real")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Skip("Cannot create symlinks on this system (may require admin on Windows)")
	}

	// Create subdirectory as working directory
	subdir := filepath.Join(symlinkDir, "subdir")
	if err := os.MkdirAll(subdir, 0750); err != nil {
		t.Fatal(err)
	}

	// Change to subdir (which is inside the symlinked directory)
	t.Chdir(subdir)

	// Call FindAllDatabases
	databases := FindAllDatabases()

	// Should find exactly ONE database, not two
	// Without the fix, it would find the same database twice:
	// - Once via symlink_to_real/.beads/test.db
	// - Once via real/.beads/test.db (when walking up to parent)
	if len(databases) != 1 {
		t.Errorf("expected 1 database (with deduplication), got %d", len(databases))
		for i, db := range databases {
			t.Logf("  Database %d: %s", i, db.Path)
		}
	}

	// Verify it's the database we expect
	resolvedDbPath, err := filepath.EvalSymlinks(dbPath)
	if err == nil {
		// Check if the found database matches the canonical path
		foundDbPath, err := filepath.EvalSymlinks(databases[0].Path)
		if err == nil && foundDbPath != resolvedDbPath {
			t.Errorf("expected database %s, got %s", resolvedDbPath, foundDbPath)
		}
	}
}

// TestFindAllDatabases_MultipleSymlinksToSameDB tests that multiple symlinks
// pointing to the same database are properly deduplicated
func TestFindAllDatabases_MultipleSymlinksToSameDB(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-multisymlink-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Resolve symlinks
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create structure:
	// tmpDir/
	//   real/
	//     .beads/test.db
	//   link1 -> real/
	//   link2 -> real/
	//   workdir/

	// Create real directory with database
	realDir := filepath.Join(tmpDir, "real")
	beadsDir := filepath.Join(realDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "test.db")
	if err := os.WriteFile(dbPath, []byte("fake db"), 0600); err != nil {
		t.Fatal(err)
	}

	// Create multiple symlinks
	link1 := filepath.Join(tmpDir, "link1")
	if err := os.Symlink(realDir, link1); err != nil {
		t.Skip("Cannot create symlinks on this system")
	}

	link2 := filepath.Join(tmpDir, "link2")
	if err := os.Symlink(realDir, link2); err != nil {
		t.Fatal(err)
	}

	// Create working directory
	workdir := filepath.Join(tmpDir, "workdir")
	if err := os.MkdirAll(workdir, 0750); err != nil {
		t.Fatal(err)
	}

	// Change to working directory
	t.Chdir(workdir)

	// Find databases
	databases := FindAllDatabases()

	// Should find exactly 1 database (all paths resolve to the same real database)
	if len(databases) != 1 {
		t.Errorf("expected 1 database with deduplication, got %d", len(databases))
		for i, db := range databases {
			t.Logf("  Database %d: %s", i, db.Path)
		}
	}
}
