package fix

import (
	"os"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// TestFixFunctions_RequireBeadsDir verifies all fix functions properly validate
// that a .beads directory exists before attempting fixes.
// This replaces 10+ individual "missing .beads directory" subtests.
func TestFixFunctions_RequireBeadsDir(t *testing.T) {
	funcs := []struct {
		name string
		fn   func(string) error
	}{
		{"GitHooks", GitHooks},
		{"MergeDriver", MergeDriver},
		{"Daemon", Daemon},
		{"DBJSONLSync", DBJSONLSync},
		{"DatabaseVersion", DatabaseVersion},
		{"SchemaCompatibility", SchemaCompatibility},
		{"SyncBranchConfig", SyncBranchConfig},
		{"SyncBranchHealth", func(dir string) error { return SyncBranchHealth(dir, "beads-sync") }},
		{"UntrackedJSONL", UntrackedJSONL},
		{"MigrateTombstones", MigrateTombstones},
		{"ChildParentDependencies", func(dir string) error { return ChildParentDependencies(dir, false) }},
		{"OrphanedDependencies", func(dir string) error { return OrphanedDependencies(dir, false) }},
	}

	for _, tc := range funcs {
		t.Run(tc.name, func(t *testing.T) {
			// Use a temp directory without .beads
			dir := t.TempDir()
			err := tc.fn(dir)
			if err == nil {
				t.Errorf("%s should return error for missing .beads directory", tc.name)
			}
		})
	}
}

func TestChildParentDependencies_NoBadDeps(t *testing.T) {
	// Set up test database with no child→parent deps
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// Create minimal schema
	_, err = db.Exec(`
		CREATE TABLE issues (id TEXT PRIMARY KEY);
		CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, type TEXT);
		CREATE TABLE dirty_issues (issue_id TEXT PRIMARY KEY);
		INSERT INTO issues (id) VALUES ('bd-abc'), ('bd-abc.1'), ('bd-xyz');
		INSERT INTO dependencies (issue_id, depends_on_id, type) VALUES ('bd-abc.1', 'bd-xyz', 'blocks');
	`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Run fix - should find no bad deps
	err = ChildParentDependencies(dir, false)
	if err != nil {
		t.Errorf("ChildParentDependencies failed: %v", err)
	}

	// Verify the good dependency still exists
	db, _ = openDB(dbPath)
	defer db.Close()
	var count int
	db.QueryRow("SELECT COUNT(*) FROM dependencies").Scan(&count)
	if count != 1 {
		t.Errorf("Expected 1 dependency, got %d", count)
	}
}

func TestChildParentDependencies_FixesBadDeps(t *testing.T) {
	// Set up test database with child→parent deps
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// Create minimal schema with child→parent dependency
	_, err = db.Exec(`
		CREATE TABLE issues (id TEXT PRIMARY KEY);
		CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, type TEXT);
		CREATE TABLE dirty_issues (issue_id TEXT PRIMARY KEY);
		INSERT INTO issues (id) VALUES ('bd-abc'), ('bd-abc.1'), ('bd-abc.1.2');
		INSERT INTO dependencies (issue_id, depends_on_id, type) VALUES
			('bd-abc.1', 'bd-abc', 'blocks'),
			('bd-abc.1.2', 'bd-abc', 'blocks'),
			('bd-abc.1.2', 'bd-abc.1', 'blocks');
	`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Run fix
	err = ChildParentDependencies(dir, false)
	if err != nil {
		t.Errorf("ChildParentDependencies failed: %v", err)
	}

	// Verify all bad dependencies were removed
	db, _ = openDB(dbPath)
	defer db.Close()
	var count int
	db.QueryRow("SELECT COUNT(*) FROM dependencies").Scan(&count)
	if count != 0 {
		t.Errorf("Expected 0 dependencies after fix, got %d", count)
	}

	// Verify dirty_issues was updated for affected issues
	// Note: 2 unique issue_ids (bd-abc.1 appears once, bd-abc.1.2 appears twice but INSERT OR IGNORE dedupes)
	var dirtyCount int
	db.QueryRow("SELECT COUNT(*) FROM dirty_issues").Scan(&dirtyCount)
	if dirtyCount != 2 {
		t.Errorf("Expected 2 dirty issues (unique issue_ids from removed deps), got %d", dirtyCount)
	}
}

// TestChildParentDependencies_PreservesParentChildType verifies that legitimate
// parent-child type dependencies are NOT removed (only blocking types are removed).
// Regression test for GitHub issue #750.
func TestChildParentDependencies_PreservesParentChildType(t *testing.T) {
	// Set up test database with both 'blocks' and 'parent-child' type deps
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// Create schema with both 'blocks' (anti-pattern) and 'parent-child' (legitimate) deps
	_, err = db.Exec(`
		CREATE TABLE issues (id TEXT PRIMARY KEY);
		CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, type TEXT);
		CREATE TABLE dirty_issues (issue_id TEXT PRIMARY KEY);
		INSERT INTO issues (id) VALUES ('bd-abc'), ('bd-abc.1'), ('bd-abc.2');
		INSERT INTO dependencies (issue_id, depends_on_id, type) VALUES
			('bd-abc.1', 'bd-abc', 'parent-child'),
			('bd-abc.2', 'bd-abc', 'parent-child'),
			('bd-abc.1', 'bd-abc', 'blocks');
	`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Run fix
	err = ChildParentDependencies(dir, false)
	if err != nil {
		t.Fatalf("ChildParentDependencies failed: %v", err)
	}

	// Verify only 'blocks' type was removed, 'parent-child' preserved
	db, _ = openDB(dbPath)
	defer db.Close()

	var blocksCount int
	db.QueryRow("SELECT COUNT(*) FROM dependencies WHERE type = 'blocks'").Scan(&blocksCount)
	if blocksCount != 0 {
		t.Errorf("Expected 0 'blocks' dependencies after fix, got %d", blocksCount)
	}

	var parentChildCount int
	db.QueryRow("SELECT COUNT(*) FROM dependencies WHERE type = 'parent-child'").Scan(&parentChildCount)
	if parentChildCount != 2 {
		t.Errorf("Expected 2 'parent-child' dependencies preserved, got %d", parentChildCount)
	}

	// Verify only 1 dirty issue (the one with 'blocks' dep removed)
	var dirtyCount int
	db.QueryRow("SELECT COUNT(*) FROM dirty_issues").Scan(&dirtyCount)
	if dirtyCount != 1 {
		t.Errorf("Expected 1 dirty issue, got %d", dirtyCount)
	}
}
