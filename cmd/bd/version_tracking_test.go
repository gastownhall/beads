//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

func TestGetVersionsSince(t *testing.T) {
	// Get current version counts dynamically from versionChanges
	latestVersion := versionChanges[0].Version                     // First element is latest
	oldestVersion := versionChanges[len(versionChanges)-1].Version // Last element is oldest
	versionsAfterOldest := len(versionChanges) - 1                 // All except oldest

	tests := []struct {
		name          string
		sinceVersion  string
		expectedCount int
		description   string
	}{
		{
			name:          "empty version returns all",
			sinceVersion:  "",
			expectedCount: len(versionChanges),
			description:   "Should return all versions when sinceVersion is empty",
		},
		{
			name:          "version not in changelog",
			sinceVersion:  "0.1.0",
			expectedCount: len(versionChanges),
			description:   "Should return all versions when sinceVersion not found",
		},
		{
			name:          "oldest version in changelog",
			sinceVersion:  oldestVersion,
			expectedCount: versionsAfterOldest,
			description:   "Should return versions newer than oldest",
		},
		{
			name:          "latest version returns empty",
			sinceVersion:  latestVersion,
			expectedCount: 0,
			description:   "Should return empty slice when already on latest in changelog",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getVersionsSince(tt.sinceVersion)
			if len(result) != tt.expectedCount {
				t.Errorf("getVersionsSince(%q) returned %d versions, want %d: %s",
					tt.sinceVersion, len(result), tt.expectedCount, tt.description)
			}
		})
	}
}

func TestGetVersionsSinceOrder(t *testing.T) {
	// Test that versions are returned in chronological order (oldest first)
	// versionChanges array is newest-first, but getVersionsSince returns oldest-first
	oldestVersion := versionChanges[len(versionChanges)-1].Version
	result := getVersionsSince(oldestVersion)

	expectedCount := len(versionChanges) - 1
	if len(result) != expectedCount {
		t.Fatalf("Expected %d versions after %s, got %d", expectedCount, oldestVersion, len(result))
	}

	// Verify chronological order by checking dates increase (or are equal for same-day releases)
	for i := 1; i < len(result); i++ {
		prev := result[i-1]
		curr := result[i]

		// Simple date comparison (YYYY-MM-DD format)
		if curr.Date < prev.Date {
			t.Errorf("Versions not in chronological order: %s (%s) should come before %s (%s)",
				prev.Version, prev.Date, curr.Version, curr.Date)
		}
	}

	// First version after oldest should be second-to-last in versionChanges
	// Last version should be the first in versionChanges (latest)
	if len(result) > 0 {
		expectedFirst := versionChanges[len(versionChanges)-2].Version
		expectedLast := versionChanges[0].Version
		if result[0].Version != expectedFirst {
			t.Errorf("First version = %s, want %s", result[0].Version, expectedFirst)
		}
		if result[len(result)-1].Version != expectedLast {
			t.Errorf("Last version = %s, want %s", result[len(result)-1].Version, expectedLast)
		}
	}
}

func TestTrackBdVersion_NoBeadsDir(t *testing.T) {
	// Reset global state for test isolation
	ensureCleanGlobalState(t)

	// Save original state
	origUpgradeDetected := versionUpgradeDetected
	origPreviousVersion := previousVersion
	defer func() {
		versionUpgradeDetected = origUpgradeDetected
		previousVersion = origPreviousVersion
	}()

	// Reset state to ensure clean starting point
	versionUpgradeDetected = false
	previousVersion = ""

	// Change to temp directory with no .beads
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Reset git caches so IsWorktree() returns fresh results for the temp dir
	git.ResetCaches()

	// Set BEADS_DIR to temp directory to prevent FindBeadsDir from walking up
	// or finding the worktree's main repository .beads directory
	t.Setenv("BEADS_DIR", tmpDir)

	// trackBdVersion should silently succeed
	trackBdVersion()

	// Should not detect upgrade when no .beads dir exists
	if versionUpgradeDetected {
		t.Error("Expected no upgrade detection when .beads directory doesn't exist")
	}
}

func TestTrackBdVersion_FirstRun(t *testing.T) {
	// Reset global state for test isolation
	ensureCleanGlobalState(t)

	// Create temp .beads directory with a project file (bd-420)
	// FindBeadsDir now requires actual project files, not just directory existence
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads: %v", err)
	}
	// Create a database file so FindBeadsDir finds this directory
	dbPath := filepath.Join(beadsDir, "beads.db")
	if err := os.WriteFile(dbPath, []byte{}, 0644); err != nil {
		t.Fatalf("Failed to create db file: %v", err)
	}

	// Set BEADS_DIR to force FindBeadsDir to use our temp directory
	// This prevents finding the actual .beads in a git worktree
	t.Setenv("BEADS_DIR", beadsDir)

	// Change to temp directory
	t.Chdir(tmpDir)

	// Save original state
	origUpgradeDetected := versionUpgradeDetected
	origPreviousVersion := previousVersion
	defer func() {
		versionUpgradeDetected = origUpgradeDetected
		previousVersion = origPreviousVersion
	}()

	// Reset state
	versionUpgradeDetected = false
	previousVersion = ""

	// trackBdVersion should create .local_version
	trackBdVersion()

	// Should not detect upgrade on first run
	if versionUpgradeDetected {
		t.Error("Expected no upgrade detection on first run")
	}

	// Should have created .local_version with current version
	localVersionPath := filepath.Join(beadsDir, localVersionFile)
	localVersion := readLocalVersion(localVersionPath)
	if localVersion != Version {
		t.Errorf(".local_version = %q, want %q", localVersion, Version)
	}
}

func TestTrackBdVersion_UpgradeDetection(t *testing.T) {
	// Reset global state for test isolation
	ensureCleanGlobalState(t)

	// Create temp .beads directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads: %v", err)
	}

	// Set BEADS_DIR to force FindBeadsDir to use our temp directory
	// This prevents finding the actual .beads in a git worktree
	t.Setenv("BEADS_DIR", beadsDir)

	// Change to temp directory
	t.Chdir(tmpDir)

	// Create minimal metadata.json so FindBeadsDir can find the directory (bd-420)
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"database":"beads.db"}`), 0600); err != nil {
		t.Fatalf("Failed to create metadata.json: %v", err)
	}

	// Create .local_version with old version (simulating previous bd run)
	localVersionPath := filepath.Join(beadsDir, localVersionFile)
	if err := writeLocalVersion(localVersionPath, "0.22.0"); err != nil {
		t.Fatalf("Failed to write local version: %v", err)
	}

	// Save original state
	origUpgradeDetected := versionUpgradeDetected
	origPreviousVersion := previousVersion
	defer func() {
		versionUpgradeDetected = origUpgradeDetected
		previousVersion = origPreviousVersion
	}()

	// Reset state
	versionUpgradeDetected = false
	previousVersion = ""

	// trackBdVersion should detect upgrade
	trackBdVersion()

	// Should detect upgrade
	if !versionUpgradeDetected {
		t.Error("Expected upgrade detection when version changed")
	}

	if previousVersion != "0.22.0" {
		t.Errorf("previousVersion = %q, want %q", previousVersion, "0.22.0")
	}

	// Should have updated .local_version to current version
	localVersion := readLocalVersion(localVersionPath)
	if localVersion != Version {
		t.Errorf(".local_version = %q, want %q", localVersion, Version)
	}
}

func TestTrackBdVersion_DowngradeIgnored(t *testing.T) {
	// Reset global state for test isolation
	ensureCleanGlobalState(t)

	// Create temp .beads directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads: %v", err)
	}

	// Set BEADS_DIR to force FindBeadsDir to use our temp directory
	t.Setenv("BEADS_DIR", beadsDir)

	// Change to temp directory
	t.Chdir(tmpDir)

	// Create minimal metadata.json so FindBeadsDir can find the directory
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"database":"beads.db"}`), 0600); err != nil {
		t.Fatalf("Failed to create metadata.json: %v", err)
	}

	// Create .local_version with a NEWER version than current (simulating downgrade)
	localVersionPath := filepath.Join(beadsDir, localVersionFile)
	if err := writeLocalVersion(localVersionPath, "99.99.99"); err != nil {
		t.Fatalf("Failed to write local version: %v", err)
	}

	// Save original state
	origUpgradeDetected := versionUpgradeDetected
	origPreviousVersion := previousVersion
	defer func() {
		versionUpgradeDetected = origUpgradeDetected
		previousVersion = origPreviousVersion
	}()

	// Reset state
	versionUpgradeDetected = false
	previousVersion = ""

	// trackBdVersion should NOT detect upgrade (this is a downgrade)
	trackBdVersion()

	if versionUpgradeDetected {
		t.Error("Expected no upgrade detection when version is a downgrade")
	}

	if previousVersion != "" {
		t.Errorf("previousVersion = %q, want empty string for downgrade", previousVersion)
	}

	// Should still update .local_version to current version
	localVersion := readLocalVersion(localVersionPath)
	if localVersion != Version {
		t.Errorf(".local_version = %q, want %q", localVersion, Version)
	}
}

func TestTrackBdVersion_SameVersion(t *testing.T) {
	// Create temp .beads directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads: %v", err)
	}

	// Change to temp directory
	t.Chdir(tmpDir)

	// Create .local_version with current version
	localVersionPath := filepath.Join(beadsDir, localVersionFile)
	if err := writeLocalVersion(localVersionPath, Version); err != nil {
		t.Fatalf("Failed to write local version: %v", err)
	}

	// Save original state
	origUpgradeDetected := versionUpgradeDetected
	origPreviousVersion := previousVersion
	defer func() {
		versionUpgradeDetected = origUpgradeDetected
		previousVersion = origPreviousVersion
	}()

	// Reset state
	versionUpgradeDetected = false
	previousVersion = ""

	// trackBdVersion should not detect upgrade
	trackBdVersion()

	// Should not detect upgrade
	if versionUpgradeDetected {
		t.Error("Expected no upgrade detection when version is the same")
	}
}

func TestMaybeShowUpgradeNotification(t *testing.T) {
	// Save original state
	origUpgradeDetected := versionUpgradeDetected
	origPreviousVersion := previousVersion
	origUpgradeAcknowledged := upgradeAcknowledged
	defer func() {
		versionUpgradeDetected = origUpgradeDetected
		previousVersion = origPreviousVersion
		upgradeAcknowledged = origUpgradeAcknowledged
	}()

	// Test: No upgrade detected - should not modify acknowledged flag
	versionUpgradeDetected = false
	upgradeAcknowledged = false
	previousVersion = ""

	maybeShowUpgradeNotification()
	if upgradeAcknowledged {
		t.Error("Should not set acknowledged flag when no upgrade detected")
	}

	// Test: Upgrade detected but already acknowledged - should not change state
	versionUpgradeDetected = true
	upgradeAcknowledged = true
	previousVersion = "0.22.0"

	maybeShowUpgradeNotification()
	if !upgradeAcknowledged {
		t.Error("Should keep acknowledged flag when already acknowledged")
	}

	// Test: Upgrade detected and not acknowledged - should set acknowledged flag
	versionUpgradeDetected = true
	upgradeAcknowledged = false
	previousVersion = "0.22.0"

	maybeShowUpgradeNotification()
	if !upgradeAcknowledged {
		t.Error("Should mark as acknowledged after showing notification")
	}

	// Calling again should keep acknowledged flag set
	prevAck := upgradeAcknowledged
	maybeShowUpgradeNotification()
	if upgradeAcknowledged != prevAck {
		t.Error("Should not change acknowledged state on subsequent calls")
	}
}

func TestAutoMigrateOnVersionBump_NoUpgrade(t *testing.T) {
	// Save original state
	origUpgradeDetected := versionUpgradeDetected
	defer func() {
		versionUpgradeDetected = origUpgradeDetected
	}()

	// Reset state - no upgrade detected
	versionUpgradeDetected = false

	// Should return early without doing anything
	autoMigrateOnVersionBump(t.TempDir())

	// Test passes if no panic occurs
}

func TestAutoMigrateOnVersionBump_NoDatabase(t *testing.T) {
	// Create temp directory (no database file inside)
	tmpDir := t.TempDir()

	// Save original state
	origUpgradeDetected := versionUpgradeDetected
	defer func() {
		versionUpgradeDetected = origUpgradeDetected
	}()

	// Simulate version upgrade
	versionUpgradeDetected = true

	// Should handle gracefully when database doesn't exist
	autoMigrateOnVersionBump(tmpDir)

	// Test passes if no panic occurs
}

func TestAutoMigrateOnVersionBump_MigratesVersion(t *testing.T) {
	// NOTE: Cannot use t.Parallel() because we modify global variables

	// Save original state FIRST - critical to avoid test pollution from previous tests
	origUpgradeDetected := versionUpgradeDetected
	origUpgradeAcknowledged := upgradeAcknowledged
	origPreviousVersion := previousVersion
	defer func() {
		versionUpgradeDetected = origUpgradeDetected
		upgradeAcknowledged = origUpgradeAcknowledged
		previousVersion = origPreviousVersion
	}()

	// Create temp directory (acts as beadsDir)
	tmpDir := t.TempDir()
	dbName := "test-migrate.db"
	dbPath := filepath.Join(tmpDir, dbName)

	// Create metadata.json so factory.NewFromConfig can find the database
	cfg := &configfile.Config{Database: dbName}
	if err := cfg.Save(tmpDir); err != nil {
		t.Fatalf("Failed to create metadata.json: %v", err)
	}

	// Create database with old version
	ctx := context.Background()
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}

	// Set old database version
	oldVersion := "0.22.0"
	if err := store.SetMetadata(ctx, "bd_version", oldVersion); err != nil {
		t.Fatalf("Failed to set old version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Failed to close database: %v", err)
	}

	// Simulate version upgrade (must be set to true for migration to run)
	// Set this AFTER closing the database to ensure clean state
	upgradeAcknowledged = false
	previousVersion = oldVersion

	// Verify dbPath before migration
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("database doesn't exist before migration: %s", dbPath)
	}

	// Set versionUpgradeDetected immediately before calling to avoid races
	versionUpgradeDetected = true
	t.Logf("Calling autoMigrateOnVersionBump with beadsDir=%s, versionUpgradeDetected=%v", tmpDir, versionUpgradeDetected)
	// Immediately call migration while flag is still true
	autoMigrateOnVersionBump(tmpDir)

	// Verify the flag is still true after migration
	if !versionUpgradeDetected {
		t.Fatalf("version Upgrade detected flag was cleared during migration")
	}

	// Verify database version was updated
	store, err = sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer store.Close()

	newVersion, err := store.GetMetadata(ctx, "bd_version")
	if err != nil {
		t.Fatalf("Failed to read database version: %v", err)
	}

	if newVersion != Version {
		t.Errorf("Database version not updated: got %q, want %q (versionUpgradeDetected=%v)",
			newVersion, Version, versionUpgradeDetected)
	}
}

func TestAutoMigrateOnVersionBump_AlreadyMigrated(t *testing.T) {
	// Create temp directory (acts as beadsDir)
	tmpDir := t.TempDir()
	dbName := "test.db"
	dbPath := filepath.Join(tmpDir, dbName)

	// Create metadata.json so factory.NewFromConfig can find the database
	cfg := &configfile.Config{Database: dbName}
	if err := cfg.Save(tmpDir); err != nil {
		t.Fatalf("Failed to create metadata.json: %v", err)
	}

	// Create database with current version
	ctx := context.Background()
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}

	// Set current database version
	if err := store.SetMetadata(ctx, "bd_version", Version); err != nil {
		t.Fatalf("Failed to set version: %v", err)
	}
	_ = store.Close()

	// Save original state
	origUpgradeDetected := versionUpgradeDetected
	defer func() {
		versionUpgradeDetected = origUpgradeDetected
	}()

	// Simulate version upgrade
	versionUpgradeDetected = true

	// Call auto-migration - should be a no-op
	autoMigrateOnVersionBump(tmpDir)

	// Verify database version is still current
	store, err = sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer store.Close()

	currentVersion, err := store.GetMetadata(ctx, "bd_version")
	if err != nil {
		t.Fatalf("Failed to read database version: %v", err)
	}

	if currentVersion != Version {
		t.Errorf("Database version changed unexpectedly: got %q, want %q", currentVersion, Version)
	}
}

func TestAutoMigrateOnVersionBump_RefusesDowngrade(t *testing.T) {
	// Test that auto-migration refuses to downgrade the database version (gt-e3uiy)

	// Create temp directory (acts as beadsDir)
	tmpDir := t.TempDir()
	dbName := "test-downgrade.db"
	dbPath := filepath.Join(tmpDir, dbName)

	// Create metadata.json so factory.NewFromConfig can find the database
	cfg := &configfile.Config{Database: dbName}
	if err := cfg.Save(tmpDir); err != nil {
		t.Fatalf("Failed to create metadata.json: %v", err)
	}

	// Create database with a NEWER version than the current binary
	ctx := context.Background()
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}

	newerVersion := "99.99.99" // Always newer than any real version
	if err := store.SetMetadata(ctx, "bd_version", newerVersion); err != nil {
		t.Fatalf("Failed to set newer version: %v", err)
	}
	if err := store.SetMetadata(ctx, "bd_version_max", newerVersion); err != nil {
		t.Fatalf("Failed to set max version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Failed to close database: %v", err)
	}

	// Save original state
	origUpgradeDetected := versionUpgradeDetected
	defer func() {
		versionUpgradeDetected = origUpgradeDetected
	}()

	// Simulate version "upgrade" (actually downgrade since DB has newer version)
	versionUpgradeDetected = true

	// Call auto-migration - should refuse to downgrade
	autoMigrateOnVersionBump(tmpDir)

	// Verify database version was NOT changed (still the newer version)
	store, err = sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer store.Close()

	currentVersion, err := store.GetMetadata(ctx, "bd_version")
	if err != nil {
		t.Fatalf("Failed to read database version: %v", err)
	}

	if currentVersion != newerVersion {
		t.Errorf("Database version was downgraded: got %q, want %q (should refuse downgrade)", currentVersion, newerVersion)
	}
}

func TestAutoMigrateOnVersionBump_TracksMaxVersion(t *testing.T) {
	// Test that auto-migration tracks the max version ever applied (gt-e3uiy)

	// Save original state FIRST
	origUpgradeDetected := versionUpgradeDetected
	origUpgradeAcknowledged := upgradeAcknowledged
	origPreviousVersion := previousVersion
	defer func() {
		versionUpgradeDetected = origUpgradeDetected
		upgradeAcknowledged = origUpgradeAcknowledged
		previousVersion = origPreviousVersion
	}()

	// Create temp directory (acts as beadsDir)
	tmpDir := t.TempDir()
	dbName := "test-max-version.db"
	dbPath := filepath.Join(tmpDir, dbName)

	// Create metadata.json so factory.NewFromConfig can find the database
	cfg := &configfile.Config{Database: dbName}
	if err := cfg.Save(tmpDir); err != nil {
		t.Fatalf("Failed to create metadata.json: %v", err)
	}

	// Create database with an older version
	ctx := context.Background()
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}

	oldVersion := "0.1.0"
	if err := store.SetMetadata(ctx, "bd_version", oldVersion); err != nil {
		t.Fatalf("Failed to set old version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Failed to close database: %v", err)
	}

	// Simulate version upgrade
	versionUpgradeDetected = true
	upgradeAcknowledged = false
	previousVersion = oldVersion

	// Call auto-migration
	autoMigrateOnVersionBump(tmpDir)

	// Verify max version was set
	store, err = sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer store.Close()

	maxVersion, err := store.GetMetadata(ctx, "bd_version_max")
	if err != nil {
		t.Fatalf("Failed to read max version: %v", err)
	}

	if maxVersion != Version {
		t.Errorf("Max version not set: got %q, want %q", maxVersion, Version)
	}
}
