//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/schema"
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
		{
			name:          "brew HEAD stamp returns empty",
			sinceVersion:  "HEAD-f925f3f",
			expectedCount: 0,
			description:   "A --HEAD stamp names no changelog entry and must not dump the full history",
		},
		{
			name:          "bare brew HEAD stamp returns empty",
			sinceVersion:  "HEAD",
			expectedCount: 0,
			description:   "A bare HEAD stamp names no changelog entry and must not dump the full history",
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

func TestDisplayVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{name: "release", version: "1.3.0", want: "v1.3.0"},
		{name: "pre-release", version: "1.3.0-rc.1", want: "v1.3.0-rc.1"},
		{name: "brew HEAD stamp", version: "HEAD-f925f3f", want: "HEAD-f925f3f"},
		{name: "bare brew HEAD stamp", version: "HEAD", want: "HEAD"},
		{name: "brew HEAD stamp with revision", version: "HEAD-f925f3f_1", want: "HEAD-f925f3f_1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayVersion(tt.version); got != tt.want {
				t.Fatalf("displayVersion(%q) = %q, want %q", tt.version, got, tt.want)
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
	pending := trackBdVersion()

	// Should detect upgrade
	if !versionUpgradeDetected {
		t.Error("Expected upgrade detection when version changed")
	}

	if previousVersion != "0.22.0" {
		t.Errorf("previousVersion = %q, want %q", previousVersion, "0.22.0")
	}

	// The upgrade is held back until its maintenance settles (#6929)...
	if localVersion := readLocalVersion(localVersionPath); localVersion != "0.22.0" {
		t.Errorf(".local_version = %q before the upgrade settled, want %q", localVersion, "0.22.0")
	}
	// ...and persisting it records the current version.
	pending.persist()
	if localVersion := readLocalVersion(localVersionPath); localVersion != Version {
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

// newTrackingWorkspace prepares the minimal .beads a trackBdVersion call needs
// and pins the globals it mutates, returning the .local_version path.
func newTrackingWorkspace(t *testing.T, lastVersion, binVersion string) string {
	t.Helper()
	ensureCleanGlobalState(t)

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Chdir(tmpDir)

	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"database":"beads.db"}`), 0600); err != nil {
		t.Fatalf("Failed to create metadata.json: %v", err)
	}

	localVersionPath := filepath.Join(beadsDir, localVersionFile)
	if err := writeLocalVersion(localVersionPath, lastVersion); err != nil {
		t.Fatalf("Failed to write local version: %v", err)
	}

	origVersion, origDetected, origPrevious := Version, versionUpgradeDetected, previousVersion
	t.Cleanup(func() {
		Version, versionUpgradeDetected, previousVersion = origVersion, origDetected, origPrevious
	})
	Version = binVersion
	versionUpgradeDetected = false
	previousVersion = ""

	return localVersionPath
}

// TestTrackBdVersion_HeadStampChangeDetectedAsUpgrade covers the stamp changes
// CompareVersions cannot see: it reads every HEAD stamp as 0.0.0, so a HEAD
// reinstall looked like no change and a release-to-HEAD move looked like a
// downgrade. Both skipped the one-shot post-upgrade reconciliation entirely.
func TestTrackBdVersion_HeadStampChangeDetectedAsUpgrade(t *testing.T) {
	tests := []struct {
		name         string
		lastVersion  string
		binVersion   string
		wantDetected bool
	}{
		{name: "HEAD stamp to different HEAD stamp", lastVersion: "HEAD-423afdc", binVersion: "HEAD-f925f3f", wantDetected: true},
		{name: "release to HEAD stamp", lastVersion: "1.1.2", binVersion: "HEAD-f925f3f", wantDetected: true},
		{name: "HEAD stamp to release", lastVersion: "HEAD-423afdc", binVersion: "1.3.0", wantDetected: true},
		{name: "same HEAD stamp", lastVersion: "HEAD-f925f3f", binVersion: "HEAD-f925f3f", wantDetected: false},
		{name: "non-HEAD garbage change stays undetected", lastVersion: "not-a-version", binVersion: "also-not-one", wantDetected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localVersionPath := newTrackingWorkspace(t, tt.lastVersion, tt.binVersion)

			pending := trackBdVersion()

			if versionUpgradeDetected != tt.wantDetected {
				t.Errorf("versionUpgradeDetected = %v, want %v", versionUpgradeDetected, tt.wantDetected)
			}
			wantPrevious := ""
			if tt.wantDetected {
				wantPrevious = tt.lastVersion
			}
			if previousVersion != wantPrevious {
				t.Errorf("previousVersion = %q, want %q", previousVersion, wantPrevious)
			}
			// A detected upgrade stays pending until its maintenance settles.
			if (pending != nil) != tt.wantDetected {
				t.Errorf("pending marker = %v, want one only for a detected upgrade", pending)
			}
			if tt.wantDetected {
				if got := readLocalVersion(localVersionPath); got != tt.lastVersion {
					t.Errorf(".local_version = %q before the upgrade settled, want %q", got, tt.lastVersion)
				}
			}
			pending.persist()
			if got := readLocalVersion(localVersionPath); got != tt.binVersion {
				t.Errorf(".local_version = %q, want %q", got, tt.binVersion)
			}
		})
	}
}

// TestTrackBdVersion_HeadStampNeverReachesPreV56Recovery walks the whole
// #5603 shape end to end: a workspace last touched by a Homebrew --HEAD build,
// a .dolt with no .bd-dolt-ok marker, and a release install on top. Detection
// and the recovery gate have to compose — widening detection is what puts a
// HEAD stamp into previousVersion in the first place.
func TestTrackBdVersion_HeadStampNeverReachesPreV56Recovery(t *testing.T) {
	newTrackingWorkspace(t, "HEAD-f925f3f", "1.3.0")
	doltDir, sentinel := writePreV56DoltFixture(t)

	trackBdVersion()

	if !versionUpgradeDetected {
		t.Fatal("installing a release over a --HEAD build should register as an upgrade")
	}
	if previousVersion != "HEAD-f925f3f" {
		t.Fatalf("previousVersion = %q, want the HEAD stamp", previousVersion)
	}

	recoverPreV56IfNeeded(previousVersion, doltDir)

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("a --HEAD predecessor routed a live workspace into the pre-v56 recovery: %v", err)
	}
}

func TestTrackBdVersion_SameVersion(t *testing.T) {
	// Create temp .beads directory
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads: %v", err)
	}

	// Override BEADS_DIR so FindBeadsDir() returns our temp .beads,
	// not the rig's .beads (which happens in worktree environments).
	t.Setenv("BEADS_DIR", beadsDir)

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

// writePreV56DoltFixture builds the workspace shape RecoverPreV56DoltDir
// destroys: a .dolt/ directory with no .bd-dolt-ok compatibility marker. It
// returns a path inside .dolt/ whose survival tells the caller whether the
// recovery ran.
func writePreV56DoltFixture(t *testing.T) (doltDir, sentinel string) {
	t.Helper()
	doltDir = t.TempDir()
	sentinel = filepath.Join(doltDir, ".dolt", "sentinel.txt")
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sentinel, []byte("live workspace data"), 0o600); err != nil {
		t.Fatal(err)
	}
	return doltDir, sentinel
}

// TestRecoverPreV56IfNeeded_OnlySemverPredecessorsAreRecovered pins the
// destructive edge from #5603/#5625/#5650: only a release older than 0.56.0
// may hand its workspace to the path that deletes .dolt.
//
//   - CompareVersions reads any unparsable version part as 0, so the Homebrew
//     --HEAD stamp compares as pre-0.56 and must fail the validity check.
//   - A Go pseudo-version passes the validity check since the GH#6152 fix,
//     and a major-0 one can compare below 0.56.0, but its numbers only bound
//     the build from below. It must be refused on its own, with or without
//     the "v".
//   - A v-prefixed release is a genuine predecessor and still recovers.
func TestRecoverPreV56IfNeeded_OnlySemverPredecessorsAreRecovered(t *testing.T) {
	tests := []struct {
		name         string
		previous     string
		wantRecovery bool
	}{
		{name: "brew HEAD stamp", previous: "HEAD-f925f3f"},
		{name: "bare brew HEAD stamp", previous: "HEAD"},
		{name: "brew HEAD stamp with revision", previous: "HEAD-f925f3f_1"},
		{name: "go pseudo-version", previous: "v1.1.1-0.20260805093327-bf97b73749ac"},
		{name: "go pseudo-version with no tag in reach", previous: "v0.0.0-20260805093327-bf97b73749ac"},
		{name: "go pseudo-version after a pre-0.56 tag", previous: "v0.55.5-0.20260101000000-abcdefabcdef"},
		{name: "go pseudo-version without its v", previous: "0.0.0-20260805093327-bf97b73749ac"},
		{name: "go pseudo-version with surrounding space", previous: " v0.0.0-20260805093327-bf97b73749ac\n"},
		{name: "unreadable witness", previous: "not-a-version"},
		{name: "no predecessor", previous: ""},
		{name: "current release", previous: "1.1.2"},
		{name: "0.56.0 itself", previous: "0.56.0"},
		{name: "pre-0.56 release", previous: "0.55.4", wantRecovery: true},
		{name: "v-prefixed pre-0.56 release", previous: "v0.55.4", wantRecovery: true},
		{name: "pre-0.56 pre-release", previous: "0.55.4-rc.1", wantRecovery: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doltDir, sentinel := writePreV56DoltFixture(t)

			recoverPreV56IfNeeded(tt.previous, doltDir)

			_, err := os.Stat(sentinel)
			if tt.wantRecovery {
				// The reinitializing `dolt init` may fail in a bare
				// environment; the removal that precedes it is the assertion.
				if !os.IsNotExist(err) {
					t.Fatalf("predecessor %q: expected pre-v56 recovery to rebuild .dolt, but %s survived", tt.previous, sentinel)
				}
				return
			}
			if err != nil {
				t.Fatalf("predecessor %q: pre-v56 recovery deleted a live .dolt: %v", tt.previous, err)
			}
		})
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
// persistence testing through autoMigrateOnVersionBump -> dolt.NewFromConfig.
// Automatic schema migration is exercised here through the Dolt backend.
// Dolt-based migration testing is covered by TestInitDoltMetadata in init_test.go.

// TestTrackBdVersion_UpgradeDefersMarkerPersist pins the first half of
// gastownhall/beads#6929: detecting an upgrade must not rewrite .local_version.
// The marker is the one-shot signal the version-bump maintenance reads, and
// writing it at detection consumed the retry of every failed upgrade.
func TestTrackBdVersion_UpgradeDefersMarkerPersist(t *testing.T) {
	localVersionPath := newTrackingWorkspace(t, "1.1.0", "1.3.0")

	trackBdVersion()

	if !versionUpgradeDetected || previousVersion != "1.1.0" {
		t.Fatalf("detection = (%v, %q), want (true, %q)", versionUpgradeDetected, previousVersion, "1.1.0")
	}
	if got := readLocalVersion(localVersionPath); got != "1.1.0" {
		t.Errorf(".local_version = %q right after detection, want the old %q kept until maintenance settles", got, "1.1.0")
	}
}

// newVersionBumpWorkspace is newTrackingWorkspace plus the local Dolt data
// directory autoMigrateOnVersionBump stats before it reaches the store, so a
// version bump in it runs the store-touching tail. It returns the .beads
// directory and the .local_version path.
func newVersionBumpWorkspace(t *testing.T, lastVersion, binVersion string) (beadsDir, localVersionPath string) {
	t.Helper()
	localVersionPath = newTrackingWorkspace(t, lastVersion, binVersion)
	beadsDir = filepath.Dir(localVersionPath)
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatalf("create local dolt data dir: %v", err)
	}
	return beadsDir, localVersionPath
}

// stubReconcileVersionBump replaces the store-touching tail of the version-bump
// maintenance with outcomes[i] for the i-th attempt (the last outcome repeats)
// and returns the attempt counter. The override is restored on cleanup.
func stubReconcileVersionBump(t *testing.T, outcomes ...error) *int {
	t.Helper()
	attempts := 0
	orig := reconcileVersionBump
	reconcileVersionBump = func(context.Context, string) error {
		i := attempts
		attempts++
		if i >= len(outcomes) {
			i = len(outcomes) - 1
		}
		return outcomes[i]
	}
	t.Cleanup(func() { reconcileVersionBump = orig })
	return &attempts
}

// invokeVersionBump runs the pre-run's version sequence the way one bd
// invocation does: fresh detection state, detection, then the maintenance
// that settles (or keeps pending) the marker.
func invokeVersionBump(beadsDir string) {
	versionUpgradeDetected, previousVersion = false, ""
	pending := trackBdVersion()
	runVersionBumpMaintenance(pending, beadsDir)
}

// TestVersionBump_DirtyTablesKeepsMarkerAndRetries is gastownhall/beads#6929
// across invocations: a maintenance failure that is not a deliberate deferral
// keeps the old marker, so the next invocation detects the same upgrade and
// tries again; once an attempt succeeds the marker advances and the
// maintenance stops.
func TestVersionBump_DirtyTablesKeepsMarkerAndRetries(t *testing.T) {
	pinJSONOutput(t, true) // keep any notice off the test's stderr
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "dirty tables", err: fmt.Errorf("failed to open database: %w", &schema.DirtyTablesError{Tables: []string{"issues"}})},
		{name: "generic open failure", err: fmt.Errorf("failed to open database: %w", errors.New("connection refused"))},
		{name: "version markers unavailable", err: fmt.Errorf("version markers unavailable: %w", errors.New("no role"))},
		{name: "reconcile failure", err: fmt.Errorf("failed to reconcile database version: %w", errors.New("write failed"))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir, localVersionPath := newVersionBumpWorkspace(t, "1.1.0", "1.3.0")
			attempts := stubReconcileVersionBump(t, tt.err, tt.err, nil)

			for i := 1; i <= 2; i++ {
				invokeVersionBump(beadsDir)
				if got := readLocalVersion(localVersionPath); got != "1.1.0" {
					t.Fatalf("invocation %d: .local_version = %q after a failed maintenance, want the old %q kept", i, got, "1.1.0")
				}
				if *attempts != i {
					t.Fatalf("invocation %d: %d maintenance attempts, want %d (each invocation retries)", i, *attempts, i)
				}
			}

			invokeVersionBump(beadsDir)
			if got := readLocalVersion(localVersionPath); got != "1.3.0" {
				t.Fatalf(".local_version = %q after a successful retry, want %q", got, "1.3.0")
			}

			invokeVersionBump(beadsDir)
			if *attempts != 3 {
				t.Errorf("%d maintenance attempts after the upgrade settled, want 3 (no further retries)", *attempts)
			}
		})
	}

	t.Run("malformed metadata", func(t *testing.T) {
		beadsDir, localVersionPath := newVersionBumpWorkspace(t, "1.1.0", "1.3.0")
		attempts := stubReconcileVersionBump(t, nil)
		metadataPath := filepath.Join(beadsDir, "metadata.json")
		if err := os.WriteFile(metadataPath, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}

		for i := 1; i <= 2; i++ {
			invokeVersionBump(beadsDir)
			if got := readLocalVersion(localVersionPath); got != "1.1.0" {
				t.Fatalf("invocation %d: .local_version = %q after an unreadable config, want the old %q kept", i, got, "1.1.0")
			}
		}

		if err := os.WriteFile(metadataPath, []byte(`{"database":"beads.db"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		invokeVersionBump(beadsDir)
		if got := readLocalVersion(localVersionPath); got != "1.3.0" {
			t.Fatalf(".local_version = %q after the config was repaired, want %q", got, "1.3.0")
		}
		if *attempts != 1 {
			t.Errorf("%d maintenance attempts, want 1 (the unreadable config never reached the store)", *attempts)
		}
	})
}

// TestFinishBdVersionTrackingConsumesMarkerOnlyWhenAllowed is the contributor's
// marker regression from gastownhall/beads#6949, adapted to the pending-marker
// helper: a maintenance that did not settle keeps the old marker, and the one
// that settles writes the held-back version.
func TestFinishBdVersionTrackingConsumesMarkerOnlyWhenAllowed(t *testing.T) {
	pinJSONOutput(t, true) // keep any notice off the test's stderr
	beadsDir, marker := newVersionBumpWorkspace(t, "1.1.0", "1.3.0")
	stubReconcileVersionBump(t, errors.New("connection refused"), nil)
	versionUpgradeDetected, previousVersion = true, "1.1.0"
	pending := &pendingVersionMarker{path: marker, version: Version}

	runVersionBumpMaintenance(pending, beadsDir)
	if got := readLocalVersion(marker); got != "1.1.0" {
		t.Fatalf("failed reconciliation changed marker to %q, want old version", got)
	}

	runVersionBumpMaintenance(pending, beadsDir)
	if got := readLocalVersion(marker); got != Version {
		t.Fatalf("successful reconciliation left marker at %q, want %q", got, Version)
	}
}

// captureVersionBumpOutput runs fn with stdout and stderr redirected and
// returns what each received. The notices it is used for are a few lines, well
// under a pipe's buffer, so reading after fn returns cannot block.
func captureVersionBumpOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW
	fn()
	os.Stdout, os.Stderr = origOut, origErr
	_ = outW.Close()
	_ = errW.Close()

	read := func(r *os.File) string {
		defer r.Close()
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	return read(outR), read(errR)
}

// TestVersionBump_DirtyTablesNotice covers the human recovery path for a
// dirty-table refusal during version-bump maintenance (gastownhall/beads#6929):
// it used to reach only BD_DEBUG. The notice names the tables and the
// workspace the maintenance opened, and prescribes committing before
// migrating. Under --json it writes nothing on either stream.
func TestVersionBump_DirtyTablesNotice(t *testing.T) {
	dirty := fmt.Errorf("failed to open database: %w", &schema.DirtyTablesError{Tables: []string{"issues", "wisps"}})

	for _, tt := range []struct {
		name   string
		json   bool
		global bool
	}{
		{name: "human"},
		{name: "human with globalFlag set", global: true},
		{name: "json", json: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pinJSONOutput(t, tt.json)
			origGlobal := globalFlag
			globalFlag = tt.global
			t.Cleanup(func() { globalFlag = origGlobal })

			beadsDir, _ := newVersionBumpWorkspace(t, "1.1.0", "1.3.0")
			stubReconcileVersionBump(t, dirty)

			stdout, stderr := captureVersionBumpOutput(t, func() { invokeVersionBump(beadsDir) })

			if stdout != "" {
				t.Errorf("the notice wrote to stdout:\n%s", stdout)
			}
			if tt.json {
				if stderr != "" {
					t.Errorf("under --json the notice must write nothing, got stderr:\n%s", stderr)
				}
				return
			}
			for _, want := range []string{"issues, wisps", beadsDir, "bd dolt commit", "bd migrate schema"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("notice missing %q:\n%s", want, stderr)
				}
			}
			if strings.Index(stderr, "bd dolt commit") > strings.Index(stderr, "bd migrate schema") {
				t.Errorf("notice must prescribe the commit before the migration:\n%s", stderr)
			}
			// The maintenance opened beadsDir's own database, not the one a
			// later --global override selects, so its remedy must not
			// redirect the operator to the global store.
			if strings.Contains(stderr, "--global") {
				t.Errorf("notice redirects the remedy to --global, which is not the workspace the maintenance opened:\n%s", stderr)
			}
		})
	}
}

// TestNoticeDirtyTablesRefusal pins that the dirty-table notice fires only for
// a dirty-table refusal: any other maintenance failure stays on BD_DEBUG.
func TestNoticeDirtyTablesRefusal(t *testing.T) {
	pinJSONOutput(t, false) // a json leak from another test must not read as silence
	t.Run("untyped error prints nothing", func(t *testing.T) {
		out := captureNoticeStderr(t, func() { noticeDirtyTablesRefusal(errors.New("connection refused"), "/ws/.beads") })
		if out != "" {
			t.Errorf("only a dirty-table refusal should produce a notice, got:\n%s", out)
		}
	})
}

// TestVersionBump_DeferralSettlesMarker covers the deliberate deferrals: a
// writable open refused by the remote/shared migrate gate or by missing
// migration consent is an operator decision, not a failure to retry. The bump
// settles, the next invocation does not repeat the maintenance, and the
// pre-run's gate diagnostic is the one noticeSharedMigrateRefusal already
// prints for that decision.
func TestVersionBump_DeferralSettlesMarker(t *testing.T) {
	pinJSONOutput(t, false)
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "shared store without remote", err: &schema.RemoteMigrateGateError{CurrentVersion: 72, LatestVersion: 74, Pending: 2, Decision: "shared-no-remote"}},
		{name: "remote-backed stop", err: &schema.RemoteMigrateGateError{CurrentVersion: 72, LatestVersion: 74, Pending: 2}},
		{name: "adopt", err: &schema.RemoteMigrateGateError{CurrentVersion: 72, LatestVersion: 74, Pending: 2, Decision: "adopt"}},
		{name: "migration consent", err: &schema.MigrateConsentError{CurrentVersion: 72, LatestVersion: 74, Pending: 2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir, localVersionPath := newVersionBumpWorkspace(t, "1.1.0", "1.3.0")
			attempts := stubReconcileVersionBump(t, fmt.Errorf("failed to open database: %w", tt.err), nil)
			_, wantNotice := captureVersionBumpOutput(t, func() { noticeSharedMigrateRefusal(tt.err) })

			_, stderr := captureVersionBumpOutput(t, func() { invokeVersionBump(beadsDir) })

			if got := readLocalVersion(localVersionPath); got != "1.3.0" {
				t.Fatalf(".local_version = %q after a deliberate deferral, want %q (the deferral settles the bump)", got, "1.3.0")
			}
			if stderr != wantNotice {
				t.Errorf("pre-run diagnostic changed:\ngot:\n%s\nwant:\n%s", stderr, wantNotice)
			}

			invokeVersionBump(beadsDir)
			if *attempts != 1 {
				t.Errorf("%d maintenance attempts, want 1 (a settled deferral is not retried)", *attempts)
			}
		})
	}
}

// TestAutoMigrateFailureConsumesVersionOnlyForDeliberateDeferral is the
// contributor's classification regression from gastownhall/beads#6949, adapted
// to versionBumpSettled: dirty-table and ordinary failures stay pending, while
// the gate and consent refusals (wrapped, as the open returns them) settle.
func TestAutoMigrateFailureConsumesVersionOnlyForDeliberateDeferral(t *testing.T) {
	if versionBumpSettled(&schema.DirtyTablesError{Tables: []string{"issues"}}) {
		t.Fatal("DirtyTablesError consumed the version marker; the next command must retry")
	}
	if versionBumpSettled(errors.New("connection refused")) {
		t.Fatal("ordinary migration failure consumed the version marker; the next command must retry")
	}
	if !versionBumpSettled(&schema.RemoteMigrateGateError{Pending: 1}) {
		t.Fatal("remote migration consent deferral did not consume the one-shot marker")
	}
	if !versionBumpSettled(fmt.Errorf("failed to open database: %w", &schema.MigrateConsentError{Pending: 1})) {
		t.Fatal("wrapped migration consent refusal did not consume the one-shot marker")
	}
}

// TestVersionBump_SettledOutcomesAdvanceMarker covers the outcomes that settle
// a version bump even though nothing was migrated: a reconciliation that ran
// (success, already current and a refused downgrade all return nil from the
// store tail), the existing not-applicable paths, and a performed pre-0.56
// recovery. That recovery deletes .dolt, and its compatibility witness is
// best-effort, so keeping the old marker after it could repeat the
// destruction on the next command. An unperformed recovery settles nothing.
// A preview detects the bump but neither writes nor reaches the maintenance.
func TestVersionBump_SettledOutcomesAdvanceMarker(t *testing.T) {
	pinJSONOutput(t, true)
	failed := fmt.Errorf("failed to open database: %w", errors.New("connection refused"))

	for _, tt := range []struct {
		name         string
		lastVersion  string
		metadata     string
		noDatabase   bool
		preV56Dolt   bool // a .dolt with no .bd-dolt-ok marker
		markedDolt   bool // a .dolt the 0.58+ marker vouches for
		tailErr      error
		wantVersion  string
		wantAttempts int
	}{
		{name: "reconciled", lastVersion: "1.1.0", wantVersion: "1.3.0", wantAttempts: 1},
		{name: "non-Dolt backend", lastVersion: "1.1.0", metadata: `{"backend":"sqlite","database":"beads.db"}`, wantVersion: "1.3.0"},
		{name: "proxied server", lastVersion: "1.1.0", metadata: `{"backend":"dolt","dolt_mode":"proxied-server"}`, wantVersion: "1.3.0"},
		{name: "no database", lastVersion: "1.1.0", noDatabase: true, wantVersion: "1.3.0"},
		{name: "performed pre-0.56 recovery, then failure", lastVersion: "0.55.4", preV56Dolt: true, tailErr: failed, wantVersion: "1.3.0", wantAttempts: 1},
		{name: "no pre-0.56 recovery, then failure", lastVersion: "0.55.4", markedDolt: true, tailErr: failed, wantVersion: "0.55.4", wantAttempts: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir, localVersionPath := newVersionBumpWorkspace(t, tt.lastVersion, "1.3.0")
			attempts := stubReconcileVersionBump(t, tt.tailErr)
			doltDir := filepath.Join(beadsDir, "dolt")
			if tt.metadata != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(tt.metadata), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tt.noDatabase {
				if err := os.RemoveAll(doltDir); err != nil {
					t.Fatal(err)
				}
			}
			if tt.preV56Dolt || tt.markedDolt {
				if err := os.MkdirAll(filepath.Join(doltDir, ".dolt"), 0o750); err != nil {
					t.Fatal(err)
				}
				// No dolt on PATH: the recovery's re-init fails after the
				// removal, the true-plus-error outcome, and never touches a
				// real dolt.
				t.Setenv("PATH", "")
			}
			if tt.markedDolt {
				if err := os.WriteFile(filepath.Join(doltDir, ".bd-dolt-ok"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			invokeVersionBump(beadsDir)

			if got := readLocalVersion(localVersionPath); got != tt.wantVersion {
				t.Errorf(".local_version = %q, want %q", got, tt.wantVersion)
			}
			if *attempts != tt.wantAttempts {
				t.Errorf("%d store attempts, want %d", *attempts, tt.wantAttempts)
			}
			if tt.preV56Dolt {
				if _, err := os.Stat(filepath.Join(doltDir, ".dolt")); !os.IsNotExist(err) {
					t.Errorf("fixture precondition: the pre-0.56 recovery did not run (.dolt survived: %v)", err)
				}
			}
		})
	}

	t.Run("preview", func(t *testing.T) {
		_, localVersionPath := newVersionBumpWorkspace(t, "1.1.0", "1.3.0")
		attempts := stubReconcileVersionBump(t, nil)

		// The pre-run's preview arm: detection only, and no maintenance call.
		trackBdVersionPreview()

		if !versionUpgradeDetected {
			t.Error("a preview should still detect the upgrade")
		}
		if got := readLocalVersion(localVersionPath); got != "1.1.0" {
			t.Errorf(".local_version = %q after a preview, want the old %q", got, "1.1.0")
		}
		if *attempts != 0 {
			t.Errorf("%d store attempts during a preview, want 0", *attempts)
		}
	})
}

// TestDoctorVersionMaintenance_ScopesMarkerToTarget covers `bd doctor <path>`
// on a workspace other than the caller's: its version maintenance runs against
// the target, keeps or advances only the target's marker by the same rules as
// the pre-run, restores BEADS_DIR (set or unset) and never writes the
// caller's marker.
func TestDoctorVersionMaintenance_ScopesMarkerToTarget(t *testing.T) {
	pinJSONOutput(t, true)
	failed := fmt.Errorf("failed to open database: %w", errors.New("connection refused"))

	for _, tt := range []struct {
		name          string
		callerEnvSet  bool
		tailErr       error
		wantTargetVer string
	}{
		{name: "failure, BEADS_DIR set", callerEnvSet: true, tailErr: failed, wantTargetVer: "1.1.0"},
		{name: "failure, BEADS_DIR unset", tailErr: failed, wantTargetVer: "1.1.0"},
		{name: "settled, BEADS_DIR set", callerEnvSet: true, wantTargetVer: "1.3.0"},
		{name: "settled, BEADS_DIR unset", wantTargetVer: "1.3.0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			callerBeadsDir, callerMarker := newVersionBumpWorkspace(t, "1.1.0", "1.3.0")
			if !tt.callerEnvSet {
				if err := os.Unsetenv("BEADS_DIR"); err != nil {
					t.Fatal(err)
				}
			}

			targetBeadsDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(filepath.Join(targetBeadsDir, "dolt"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(targetBeadsDir, "metadata.json"), []byte(`{"database":"beads.db"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			targetMarker := filepath.Join(targetBeadsDir, localVersionFile)
			if err := writeLocalVersion(targetMarker, "1.1.0"); err != nil {
				t.Fatal(err)
			}

			var opened []string
			orig := reconcileVersionBump
			reconcileVersionBump = func(_ context.Context, beadsDir string) error {
				opened = append(opened, beadsDir)
				return tt.tailErr
			}
			t.Cleanup(func() { reconcileVersionBump = orig })

			runDoctorVersionMaintenance(targetBeadsDir)

			if len(opened) != 1 || opened[0] != targetBeadsDir {
				t.Errorf("maintenance opened %v, want only the target %s", opened, targetBeadsDir)
			}
			if got := readLocalVersion(targetMarker); got != tt.wantTargetVer {
				t.Errorf("target .local_version = %q, want %q", got, tt.wantTargetVer)
			}
			if got := readLocalVersion(callerMarker); got != "1.1.0" {
				t.Errorf("caller .local_version = %q, want it untouched at %q", got, "1.1.0")
			}
			got, set := os.LookupEnv("BEADS_DIR")
			if set != tt.callerEnvSet || (set && got != callerBeadsDir) {
				t.Errorf("BEADS_DIR after doctor maintenance = (%q, set=%v), want (%q, set=%v)",
					got, set, callerBeadsDir, tt.callerEnvSet)
			}
		})
	}
}
