package fix

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPermissions_SkipsSymlinkedBeadsDir verifies that permission fixes are skipped
// when .beads directory is a symlink (common on NixOS with home-manager).
//
// Behavior being tested:
// - When .beads is a symlink, Permissions() should return nil without changing anything
// - This prevents attempts to chmod symlink targets (which may be read-only like /nix/store)
func TestPermissions_SkipsSymlinkedBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Create target .beads directory with wrong permissions
	targetDir := filepath.Join(tmpDir, "target-beads")
	if err := os.MkdirAll(targetDir, 0777); err != nil { // intentionally wrong permissions
		t.Fatal(err)
	}

	// Create workspace with symlinked .beads
	workspaceDir := filepath.Join(tmpDir, "workspace")
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		t.Fatal(err)
	}

	symlinkPath := filepath.Join(workspaceDir, ".beads")
	if err := os.Symlink(targetDir, symlinkPath); err != nil {
		t.Fatal(err)
	}

	// Get target's permissions before fix
	targetInfoBefore, err := os.Stat(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	permsBefore := targetInfoBefore.Mode().Perm()

	// Run Permissions fix
	err = Permissions(workspaceDir)
	if err != nil {
		t.Fatalf("Permissions() returned error for symlinked .beads: %v", err)
	}

	// Verify target's permissions were NOT changed
	targetInfoAfter, err := os.Stat(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	permsAfter := targetInfoAfter.Mode().Perm()

	if permsAfter != permsBefore {
		t.Errorf("Target directory permissions were changed through symlink!")
		t.Errorf("Before: %o, After: %o", permsBefore, permsAfter)
		t.Error("This could cause issues on NixOS where target may be in /nix/store (read-only)")
	}
}

// TestPermissions_SkipsSymlinkedDatabase verifies that chmod is skipped for
// symlinked database files, but .beads directory permissions are still fixed.
func TestPermissions_SkipsSymlinkedDatabase(t *testing.T) {
	tmpDir := t.TempDir()

	// Create real .beads directory with wrong permissions
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0777); err != nil { // intentionally wrong
		t.Fatal(err)
	}

	// Create target database file with wrong permissions
	targetDir := filepath.Join(tmpDir, "target")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	targetDB := filepath.Join(targetDir, "beads.db")
	if err := os.WriteFile(targetDB, []byte("test"), 0644); err != nil { // intentionally world-readable
		t.Fatal(err)
	}

	// Create symlink to database
	symlinkPath := filepath.Join(beadsDir, "beads.db")
	if err := os.Symlink(targetDB, symlinkPath); err != nil {
		t.Fatal(err)
	}

	// Get target's permissions before fix
	targetInfoBefore, err := os.Stat(targetDB)
	if err != nil {
		t.Fatal(err)
	}
	permsBefore := targetInfoBefore.Mode().Perm()

	// Run Permissions fix
	err = Permissions(tmpDir)
	if err != nil {
		t.Fatalf("Permissions() returned error for symlinked database: %v", err)
	}

	// Verify .beads directory permissions WERE fixed (not a symlink)
	beadsInfo, err := os.Stat(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if beadsInfo.Mode().Perm() != 0700 {
		t.Errorf("Expected .beads to have 0700 permissions, got %o", beadsInfo.Mode().Perm())
	}

	// Verify target database permissions were NOT changed (it's a symlink)
	targetInfoAfter, err := os.Stat(targetDB)
	if err != nil {
		t.Fatal(err)
	}
	permsAfter := targetInfoAfter.Mode().Perm()

	if permsAfter != permsBefore {
		t.Errorf("Target database permissions were changed through symlink!")
		t.Errorf("Before: %o, After: %o", permsBefore, permsAfter)
		t.Error("chmod should not be called on symlinked files")
	}
}

// TestPermissions_FixesRegularFiles verifies that permissions ARE fixed for
// regular (non-symlinked) files.
func TestPermissions_FixesRegularFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .beads directory with wrong permissions
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0777); err != nil { // intentionally wrong
		t.Fatal(err)
	}

	// Create database with wrong permissions
	dbPath := filepath.Join(beadsDir, "beads.db")
	if err := os.WriteFile(dbPath, []byte("test"), 0644); err != nil { // intentionally world-readable
		t.Fatal(err)
	}

	// Run Permissions fix
	err := Permissions(tmpDir)
	if err != nil {
		t.Fatalf("Permissions() failed: %v", err)
	}

	// Verify .beads directory now has 0700
	beadsInfo, err := os.Stat(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if beadsInfo.Mode().Perm() != 0700 {
		t.Errorf("Expected .beads to have 0700 permissions, got %o", beadsInfo.Mode().Perm())
	}

	// Verify database now has at least 0600 (read/write for owner)
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if dbInfo.Mode().Perm()&0600 != 0600 {
		t.Errorf("Expected database to have at least 0600 permissions, got %o", dbInfo.Mode().Perm())
	}
}
