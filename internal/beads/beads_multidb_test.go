//go:build integration
// +build integration

package beads

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindAllDatabases(t *testing.T) {
	// Create a temporary directory structure with multiple .beads databases
	tmpDir, err := os.MkdirTemp("", "beads-multidb-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Resolve symlinks (macOS /var -> /private/var)
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create nested directory structure:
	// tmpDir/
	//   .beads/test.db
	//   project1/
	//     .beads/project1.db
	//     subdir/
	//       (working directory here)

	// Root .beads
	rootBeads := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(rootBeads, 0750); err != nil {
		t.Fatal(err)
	}
	rootDB := filepath.Join(rootBeads, "test.db")
	if err := os.WriteFile(rootDB, []byte("fake db"), 0600); err != nil {
		t.Fatal(err)
	}

	// Project1 .beads
	project1Dir := filepath.Join(tmpDir, "project1")
	project1Beads := filepath.Join(project1Dir, ".beads")
	if err := os.MkdirAll(project1Beads, 0750); err != nil {
		t.Fatal(err)
	}
	project1DB := filepath.Join(project1Beads, "project1.db")
	if err := os.WriteFile(project1DB, []byte("fake db"), 0600); err != nil {
		t.Fatal(err)
	}

	// Subdir for working directory
	subdir := filepath.Join(project1Dir, "subdir")
	if err := os.MkdirAll(subdir, 0750); err != nil {
		t.Fatal(err)
	}

	// Change to subdir and test FindAllDatabases
	t.Chdir(subdir)

	databases := FindAllDatabases()

	// Should find both databases, with project1 first (closest)
	if len(databases) != 2 {
		t.Fatalf("expected 2 databases, got %d", len(databases))
	}

	// First database should be project1 (closest to CWD)
	if databases[0].Path != project1DB {
		t.Errorf("expected first database to be %s, got %s", project1DB, databases[0].Path)
	}
	if databases[0].BeadsDir != project1Beads {
		t.Errorf("expected first beads dir to be %s, got %s", project1Beads, databases[0].BeadsDir)
	}

	// Second database should be root (furthest from CWD)
	if databases[1].Path != rootDB {
		t.Errorf("expected second database to be %s, got %s", rootDB, databases[1].Path)
	}
	if databases[1].BeadsDir != rootBeads {
		t.Errorf("expected second beads dir to be %s, got %s", rootBeads, databases[1].BeadsDir)
	}
}

func TestFindAllDatabases_Single(t *testing.T) {
	// Create a temporary directory with only one database
	tmpDir, err := os.MkdirTemp("", "beads-single-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Resolve symlinks (macOS /var -> /private/var)
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create .beads directory with database
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(beadsDir, "test.db")
	if err := os.WriteFile(dbPath, []byte("fake db"), 0600); err != nil {
		t.Fatal(err)
	}

	// Change to tmpDir and test
	t.Chdir(tmpDir)

	databases := FindAllDatabases()

	// Should find exactly one database
	if len(databases) != 1 {
		t.Fatalf("expected 1 database, got %d", len(databases))
	}

	if databases[0].Path != dbPath {
		t.Errorf("expected database path %s, got %s", dbPath, databases[0].Path)
	}
}

func TestFindAllDatabases_None(t *testing.T) {
	// Create a temporary directory with no databases
	tmpDir, err := os.MkdirTemp("", "beads-none-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to tmpDir and test
	t.Chdir(tmpDir)

	databases := FindAllDatabases()

	// Should find no databases
	if len(databases) != 0 {
		t.Fatalf("expected 0 databases, got %d", len(databases))
	}
}
