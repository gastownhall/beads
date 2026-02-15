//go:build cgo

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// setupDoltMigrateWorkspace creates a temp beads workspace with a Dolt database
// and a git repo configured with a remote (needed for ComputeRepoID/GetCloneID).
// Returns (workspace root path, beadsDir, config).
func setupDoltMigrateWorkspace(t *testing.T) (string, string, *configfile.Config) {
	t.Helper()

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	// Copy cached git template (bd-ktng optimization)
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, dir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}
	cmd := exec.Command("git", "config", "remote.origin.url", "https://github.com/test/migrate-metadata.git")
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

	// Create the Dolt store via factory (bootstraps the database with schema)
	ctx := context.Background()
	doltPath := filepath.Join(beadsDir, "dolt")
	store, err := dolt.New(ctx, &dolt.Config{
		Path:     doltPath,
		Database: "beads",
	})
	if err != nil {
		t.Fatalf("failed to create Dolt store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close Dolt store: %v", err)
	}

	return dir, beadsDir, cfg
}

// saveMigrateGlobals saves package-level globals that handleDoltMetadataUpdate uses
// and returns a cleanup function to restore them. Must be deferred by caller.
func saveMigrateGlobals(t *testing.T) func() {
	t.Helper()

	oldRootCtx := rootCtx
	oldJsonOutput := jsonOutput

	return func() {
		rootCtx = oldRootCtx
		jsonOutput = oldJsonOutput
		beads.ResetCaches()
		git.ResetCaches()
	}
}

// TestMigrateDoltFullMetadata verifies that handleDoltMetadataUpdate writes
// all 3 metadata fields (bd_version, repo_id, clone_id) to a Dolt database
// that has none.
func TestMigrateDoltFullMetadata(t *testing.T) {
	cleanup := saveMigrateGlobals(t)
	defer cleanup()

	dir, beadsDir, cfg := setupDoltMigrateWorkspace(t)

	// Set globals for handleDoltMetadataUpdate
	rootCtx = context.Background()
	jsonOutput = false

	// Change to workspace dir so ComputeRepoID/GetCloneID can find the git repo
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWD) }()

	beads.ResetCaches()
	git.ResetCaches()

	// Run the function under test
	handleDoltMetadataUpdate(cfg, beadsDir, false)

	// Verify all 3 metadata fields were written
	ctx := context.Background()
	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to reopen store for verification: %v", err)
	}
	defer func() { _ = store.Close() }()

	bdVersion, err := store.GetMetadata(ctx, "bd_version")
	if err != nil {
		t.Fatalf("GetMetadata(bd_version) error: %v", err)
	}
	if bdVersion != Version {
		t.Errorf("bd_version = %q, want %q", bdVersion, Version)
	}

	repoID, err := store.GetMetadata(ctx, "repo_id")
	if err != nil {
		t.Fatalf("GetMetadata(repo_id) error: %v", err)
	}
	if repoID == "" {
		t.Error("repo_id was not set by handleDoltMetadataUpdate")
	}

	cloneID, err := store.GetMetadata(ctx, "clone_id")
	if err != nil {
		t.Fatalf("GetMetadata(clone_id) error: %v", err)
	}
	if cloneID == "" {
		t.Error("clone_id was not set by handleDoltMetadataUpdate")
	}
}

// TestMigrateDoltMetadataIdempotent verifies that running handleDoltMetadataUpdate
// on a database with all 3 fields already set does not overwrite them.
func TestMigrateDoltMetadataIdempotent(t *testing.T) {
	cleanup := saveMigrateGlobals(t)
	defer cleanup()

	dir, beadsDir, cfg := setupDoltMigrateWorkspace(t)

	rootCtx = context.Background()
	jsonOutput = false

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWD) }()

	beads.ResetCaches()
	git.ResetCaches()

	// First run: set all metadata
	handleDoltMetadataUpdate(cfg, beadsDir, false)

	// Read original values
	ctx := context.Background()
	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	origVersion, _ := store.GetMetadata(ctx, "bd_version")
	origRepoID, _ := store.GetMetadata(ctx, "repo_id")
	origCloneID, _ := store.GetMetadata(ctx, "clone_id")
	_ = store.Close()

	if origVersion == "" || origRepoID == "" || origCloneID == "" {
		t.Fatalf("first run didn't set all fields: version=%q, repo_id=%q, clone_id=%q",
			origVersion, origRepoID, origCloneID)
	}

	beads.ResetCaches()
	git.ResetCaches()

	// Second run: should be a no-op (all fields present and version matches)
	handleDoltMetadataUpdate(cfg, beadsDir, false)

	// Verify values did not change
	store2, err := dolt.NewFromConfig(ctx, beadsDir)
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

// TestMigrateDoltMetadataPartial verifies that handleDoltMetadataUpdate only
// sets missing fields and does not overwrite existing ones.
// This is the critical test for the early-return fix: when bd_version already
// matches but repo_id/clone_id are missing, the function must still set them.
func TestMigrateDoltMetadataPartial(t *testing.T) {
	cleanup := saveMigrateGlobals(t)
	defer cleanup()

	dir, beadsDir, cfg := setupDoltMigrateWorkspace(t)

	rootCtx = context.Background()
	jsonOutput = false

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWD) }()

	beads.ResetCaches()
	git.ResetCaches()

	// Pre-set only bd_version (simulate a pre-Phase-3 database that has
	// already been migrated for version but not for identity fields)
	ctx := context.Background()
	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	if err := store.SetMetadata(ctx, "bd_version", Version); err != nil {
		t.Fatalf("failed to pre-set bd_version: %v", err)
	}
	_ = store.Close()

	// Run handleDoltMetadataUpdate — the old code would early-return here
	// since bd_version matches. The new code should still set repo_id/clone_id.
	handleDoltMetadataUpdate(cfg, beadsDir, false)

	// Verify: bd_version should remain as Version (not overwritten)
	store2, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to reopen store: %v", err)
	}
	defer func() { _ = store2.Close() }()

	bdVersion, _ := store2.GetMetadata(ctx, "bd_version")
	if bdVersion != Version {
		t.Errorf("bd_version = %q, want %q (should not change)", bdVersion, Version)
	}

	// repo_id and clone_id should have been set despite bd_version already matching
	repoID, _ := store2.GetMetadata(ctx, "repo_id")
	if repoID == "" {
		t.Error("repo_id was not set during partial metadata update (early-return bug)")
	}

	cloneID, _ := store2.GetMetadata(ctx, "clone_id")
	if cloneID == "" {
		t.Error("clone_id was not set during partial metadata update (early-return bug)")
	}
}
