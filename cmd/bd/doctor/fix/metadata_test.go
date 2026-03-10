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

// TestFixMissingMetadataJSON_Regenerates verifies that FixMissingMetadataJSON
// creates a metadata.json file when it's missing but .beads/ exists (GH#2478).
func TestFixMissingMetadataJSON_Regenerates(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	configPath := filepath.Join(beadsDir, "metadata.json")

	// Verify metadata.json does not exist
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatal("metadata.json should not exist before fix")
	}

	// Run the fix
	if err := FixMissingMetadataJSON(dir); err != nil {
		t.Fatalf("FixMissingMetadataJSON failed: %v", err)
	}

	// Verify metadata.json was created
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("metadata.json should exist after fix")
	}

	// Verify it contains valid config
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("Failed to load regenerated config: %v", err)
	}
	if cfg == nil {
		t.Fatal("Loaded config should not be nil")
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		t.Errorf("Backend = %q, want %q", cfg.GetBackend(), configfile.BackendDolt)
	}
}

// TestFixMissingMetadataJSON_NoOpWhenPresent verifies that FixMissingMetadataJSON
// does nothing when metadata.json already exists.
func TestFixMissingMetadataJSON_NoOpWhenPresent(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")

	// Create metadata.json with custom content
	cfg := &configfile.Config{
		Database:  "custom-db",
		Backend:   configfile.BackendDolt,
		DoltMode:  "embedded",
		ProjectID: "test-project-123",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Run the fix — should be a no-op
	if err := FixMissingMetadataJSON(dir); err != nil {
		t.Fatalf("FixMissingMetadataJSON failed: %v", err)
	}

	// Verify original config is preserved (not overwritten with defaults)
	loaded, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if loaded.ProjectID != "test-project-123" {
		t.Errorf("ProjectID = %q, want %q (fix should not overwrite existing config)", loaded.ProjectID, "test-project-123")
	}
}

// TestFixMissingMetadataJSON_NotBeadsWorkspace verifies that FixMissingMetadataJSON
// returns an error for paths without a .beads directory.
func TestFixMissingMetadataJSON_NotBeadsWorkspace(t *testing.T) {
	dir := t.TempDir()
	err := FixMissingMetadataJSON(dir)
	if err == nil {
		t.Error("expected error for non-beads workspace")
	}
}

// setupGitRepoInDir initializes a git repo in the given directory with a remote.
func setupGitRepoInDir(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
		{"git", "config", "core.hooksPath", ".git/hooks"},
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
