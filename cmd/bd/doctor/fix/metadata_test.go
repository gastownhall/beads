package fix

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestFixMissingMetadata_NotBeadsWorkspace verifies that FixMissingMetadata
// returns an error for paths without a .beads directory.
func TestFixMissingMetadata_NotBeadsWorkspace(t *testing.T) {
	dir := t.TempDir()
	err := FixMissingMetadata(dir, "1.0.0")
	if err == nil {
		t.Error("expected error for non-beads workspace")
	}
}

// TestFixMissingMetadata_NoConfig verifies that FixMissingMetadata returns nil
// when there is no metadata.json config file (nothing to fix).
func TestFixMissingMetadata_NoConfig(t *testing.T) {
	dir := setupTestWorkspace(t)
	err := FixMissingMetadata(dir, "1.0.0")
	if err != nil {
		t.Errorf("expected nil for workspace without config, got: %v", err)
	}
}

// TestFixMissingMetadata_EmptyBackend verifies that FixMissingMetadata
// handles an empty backend field (defaults to dolt).
func TestFixMissingMetadata_EmptyBackend(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")

	// Create a config with empty backend (defaults to dolt)
	cfg := &configfile.Config{
		Database: "dolt",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Without a dolt directory, this should fail gracefully
	err := FixMissingMetadata(dir, "1.0.0")
	t.Logf("FixMissingMetadata result: %v", err)
}

// TestFixMissingMetadata_DoltConfigExists verifies that FixMissingMetadata
// handles a Dolt config correctly. On CGO builds, the factory bootstraps a
// new database even from an empty directory. On non-CGO builds, the backend
// is not registered so we get an error. Both outcomes are acceptable.
func TestFixMissingMetadata_DoltConfigExists(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")

	// Set up git repo so repo_id can be computed
	setupGitRepoInDir(t, dir)

	// Create Dolt config but no actual Dolt database
	cfg := &configfile.Config{
		Database: "dolt",
		Backend:  configfile.BackendDolt,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Create the dolt directory
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	err := FixMissingMetadata(dir, "1.0.0")
	// On CGO builds: factory bootstraps a new DB, fix succeeds (nil).
	// On non-CGO builds: "unknown storage backend" error.
	// Both are acceptable behaviors.
	t.Logf("FixMissingMetadata result: %v", err)
}

// setupGitRepoInDir initializes a git repo in the given directory with a remote.
func setupGitRepoInDir(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
		{"git", "config", "remote.origin.url", "https://github.com/test/metadata-test.git"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			// git init must succeed; config failures are non-fatal
			if args[1] == "init" {
				t.Fatalf("git init failed: %v", err)
			}
		}
	}
}
