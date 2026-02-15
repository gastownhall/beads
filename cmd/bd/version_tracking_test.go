//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/git"
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

// NOTE: TestAutoMigrateOnVersionBump_MigratesVersion, TestAutoMigrateOnVersionBump_AlreadyMigrated,
// TestAutoMigrateOnVersionBump_RefusesDowngrade, and TestAutoMigrateOnVersionBump_TracksMaxVersion
// were removed because they depended on the SQLite storage backend (sqlite.New) for round-trip
// persistence testing through autoMigrateOnVersionBump -> factory.NewFromConfig.
// Since SQLite support has been removed, auto-migration is only exercised with Dolt backends.
// Dolt-based migration testing is covered by TestInitDoltMetadata in init_test.go.
