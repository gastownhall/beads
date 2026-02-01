package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/syncbranch"
)

// TestAutoPullDefaultFromYamlConfig verifies that autoPull defaults to true
// when sync-branch is configured in config.yaml (not just SQLite).
//
// This test validates the fix for the bug where daemon.go:111-114 only checked
// store.GetConfig("sync.branch") (SQLite), but sync-branch is typically set
// in config.yaml (read via viper).
//
// Fix: daemon.go now uses syncbranch.IsConfigured() which checks env var and
// config.yaml (the common case), providing correct autoPull behavior.
func TestAutoPullDefaultFromYamlConfig(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Create config.yaml with sync-branch set (the user's configuration)
	configYAML := `# Beads configuration
sync-branch: beads-sync
issue-prefix: test
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	// Create a database WITHOUT sync.branch in the config table
	dbPath := filepath.Join(beadsDir, "beads.db")
	ctx := context.Background()
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer testStore.Close()

	// Verify: sync.branch is NOT set in SQLite
	dbSyncBranch, _ := testStore.GetConfig(ctx, "sync.branch")
	if dbSyncBranch != "" {
		t.Fatalf("Expected no sync.branch in database, got %q", dbSyncBranch)
	}

	// Reinitialize config package to read from our test directory
	// This is what happens when bd daemon starts and reads config
	// Use t.Chdir which automatically restores the original directory on test cleanup
	t.Chdir(tmpDir)

	// Reinitialize viper config to pick up the new config.yaml
	initConfigForTest(t)

	// Verify: sync.branch is NOT set in SQLite (the scenario we're testing)
	t.Logf("sync-branch in config.yaml: beads-sync")
	t.Logf("sync.branch in SQLite: %q (expected empty)", dbSyncBranch)

	// The fix: daemon.go uses syncbranch.IsConfigured() which checks env var and
	// config.yaml, not just SQLite. This should return true.
	autoPull := syncbranch.IsConfigured()
	t.Logf("syncbranch.IsConfigured() = %v", autoPull)

	if !autoPull {
		t.Errorf("Expected syncbranch.IsConfigured()=true when sync-branch is in config.yaml, got false")
	}
}

// TestAutoPullDefaultFromEnvVar verifies that env var override works correctly.
// This test should PASS because the env var is checked first.
func TestAutoPullDefaultFromEnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Create database without sync.branch
	dbPath := filepath.Join(beadsDir, "beads.db")
	ctx := context.Background()
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer testStore.Close()

	// Set env var (highest priority)
	t.Setenv("BEADS_SYNC_BRANCH", "env-sync-branch")

	// The daemon DOES check env var (line 97-98), so this path works
	// But it's in a separate code path from the YAML bug
	autoPullFromEnv := syncbranch.IsConfigured()

	if !autoPullFromEnv {
		t.Errorf("Expected autoPull=true when BEADS_SYNC_BRANCH is set, got false")
	}
}

// TestAutoPullDefaultFromSQLite verifies the legacy SQLite path still works.
// This test should PASS because the SQLite path works (it's just not used by config.yaml users).
func TestAutoPullDefaultFromSQLite(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Create database WITH sync.branch
	dbPath := filepath.Join(beadsDir, "beads.db")
	ctx := context.Background()
	testStore, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer testStore.Close()

	// Set sync.branch in SQLite (legacy configuration)
	if err := testStore.SetConfig(ctx, "sync.branch", "sqlite-sync-branch"); err != nil {
		t.Fatalf("Failed to set sync.branch in database: %v", err)
	}

	// The current daemon code should work for this case
	var autoPullFromDB bool
	if syncBranch, err := testStore.GetConfig(ctx, "sync.branch"); err == nil && syncBranch != "" {
		autoPullFromDB = true
	}

	if !autoPullFromDB {
		t.Errorf("Expected autoPull=true when sync.branch is in SQLite, got false")
	}

	// Verify syncbranch.IsConfiguredWithDB also works
	autoPullFromHelper := syncbranch.IsConfiguredWithDB(dbPath)
	if !autoPullFromHelper {
		t.Errorf("Expected IsConfiguredWithDB=true when sync.branch is in SQLite, got false")
	}
}
