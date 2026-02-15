//go:build cgo

package fix

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/factory"
)

// setupDoltWorkspace creates a temp beads workspace with a Dolt database.
// The database is initialized with the metadata table but no metadata values.
// Returns the workspace root path.
func setupDoltWorkspace(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	// Set up git repo for repo_id computation (from cached template)
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, dir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}
	cmd := exec.Command("git", "config", "remote.origin.url", "https://github.com/test/dolt-metadata-fix.git")
	cmd.Dir = dir
	_ = cmd.Run()

	// Create Dolt config
	cfg := &configfile.Config{
		Database: "dolt",
		Backend:  configfile.BackendDolt,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Create the Dolt store via factory (which bootstraps the database)
	ctx := context.Background()
	doltPath := filepath.Join(beadsDir, "dolt")
	store, err := factory.NewWithOptions(ctx, configfile.BackendDolt, doltPath, factory.Options{
		Database: "beads",
	})
	if err != nil {
		t.Fatalf("failed to create Dolt store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close Dolt store: %v", err)
	}

	return dir
}

// TestFixMissingMetadata_DoltRepair verifies that FixMissingMetadata writes
// all 3 metadata fields to a Dolt database that has none.
// Covers FR-010, FR-011, SC-004.
func TestFixMissingMetadata_DoltRepair(t *testing.T) {
	dir := setupDoltWorkspace(t)

	// Run the fix with a known version
	err := FixMissingMetadata(dir, "0.49.6")
	if err != nil {
		t.Fatalf("FixMissingMetadata failed: %v", err)
	}

	// Verify metadata was written by opening the store
	ctx := context.Background()
	beadsDir := filepath.Join(dir, ".beads")
	store, err := factory.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to reopen store for verification: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Check bd_version
	bdVersion, err := store.GetMetadata(ctx, "bd_version")
	if err != nil {
		t.Fatalf("GetMetadata(bd_version) error: %v", err)
	}
	if bdVersion != "0.49.6" {
		t.Errorf("bd_version = %q, want %q", bdVersion, "0.49.6")
	}

	// Check repo_id (should be set since we have a git repo with remote)
	repoID, err := store.GetMetadata(ctx, "repo_id")
	if err != nil {
		t.Fatalf("GetMetadata(repo_id) error: %v", err)
	}
	if repoID == "" {
		t.Error("repo_id was not set")
	}

	// Check clone_id
	cloneID, err := store.GetMetadata(ctx, "clone_id")
	if err != nil {
		t.Fatalf("GetMetadata(clone_id) error: %v", err)
	}
	if cloneID == "" {
		t.Error("clone_id was not set")
	}
}

// TestFixMissingMetadata_DoltIdempotent verifies that running FixMissingMetadata
// on a Dolt database that already has all metadata is a no-op.
// Covers FR-012, T028.
func TestFixMissingMetadata_DoltIdempotent(t *testing.T) {
	dir := setupDoltWorkspace(t)

	// First run: set all metadata
	if err := FixMissingMetadata(dir, "0.49.6"); err != nil {
		t.Fatalf("first FixMissingMetadata failed: %v", err)
	}

	// Read the values that were set
	ctx := context.Background()
	beadsDir := filepath.Join(dir, ".beads")
	store, err := factory.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	origVersion, _ := store.GetMetadata(ctx, "bd_version")
	origRepoID, _ := store.GetMetadata(ctx, "repo_id")
	origCloneID, _ := store.GetMetadata(ctx, "clone_id")
	_ = store.Close()

	// Second run: should be a no-op
	if err := FixMissingMetadata(dir, "0.50.0"); err != nil {
		t.Fatalf("second FixMissingMetadata failed: %v", err)
	}

	// Verify values did not change (version should remain 0.49.6, not 0.50.0)
	store2, err := factory.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to reopen store: %v", err)
	}
	defer func() { _ = store2.Close() }()

	newVersion, _ := store2.GetMetadata(ctx, "bd_version")
	if newVersion != origVersion {
		t.Errorf("bd_version changed from %q to %q (should be idempotent)", origVersion, newVersion)
	}

	newRepoID, _ := store2.GetMetadata(ctx, "repo_id")
	if newRepoID != origRepoID {
		t.Errorf("repo_id changed from %q to %q (should be idempotent)", origRepoID, newRepoID)
	}

	newCloneID, _ := store2.GetMetadata(ctx, "clone_id")
	if newCloneID != origCloneID {
		t.Errorf("clone_id changed from %q to %q (should be idempotent)", origCloneID, newCloneID)
	}
}

// TestFixMissingMetadata_DoltPartialRepair verifies that FixMissingMetadata
// only repairs missing fields and leaves existing ones untouched.
func TestFixMissingMetadata_DoltPartialRepair(t *testing.T) {
	dir := setupDoltWorkspace(t)

	// Pre-set only bd_version
	ctx := context.Background()
	beadsDir := filepath.Join(dir, ".beads")
	store, err := factory.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	if err := store.SetMetadata(ctx, "bd_version", "0.48.0"); err != nil {
		t.Fatalf("failed to pre-set bd_version: %v", err)
	}
	_ = store.Close()

	// Run the fix
	if err := FixMissingMetadata(dir, "0.49.6"); err != nil {
		t.Fatalf("FixMissingMetadata failed: %v", err)
	}

	// Verify: bd_version should remain 0.48.0 (not overwritten)
	store2, err := factory.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to reopen store: %v", err)
	}
	defer func() { _ = store2.Close() }()

	bdVersion, _ := store2.GetMetadata(ctx, "bd_version")
	if bdVersion != "0.48.0" {
		t.Errorf("bd_version = %q, want %q (should not overwrite existing)", bdVersion, "0.48.0")
	}

	// repo_id and clone_id should have been set
	repoID, _ := store2.GetMetadata(ctx, "repo_id")
	if repoID == "" {
		t.Error("repo_id was not set during partial repair")
	}

	cloneID, _ := store2.GetMetadata(ctx, "clone_id")
	if cloneID == "" {
		t.Error("clone_id was not set during partial repair")
	}
}
