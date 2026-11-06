//go:build cgo

package main

import (
	"errors"
	"fmt"
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
	trackBdVersion()

	// Should detect upgrade
	if !versionUpgradeDetected {
		t.Error("Expected upgrade detection when version changed")
	}

	if previousVersion != "0.22.0" {
		t.Errorf("previousVersion = %q, want %q", previousVersion, "0.22.0")
	}

	// The upgrade is written once its reconciliation settles (#6929).
	settleBdVersionStamp(versionBumpSettled)
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
	origPending, origFailed, origAck := pendingVersionStampPath, versionBumpFailedThisRun, upgradeAcknowledged
	t.Cleanup(func() {
		Version, versionUpgradeDetected, previousVersion = origVersion, origDetected, origPrevious
		pendingVersionStampPath, versionBumpFailedThisRun, upgradeAcknowledged = origPending, origFailed, origAck
	})
	Version = binVersion
	versionUpgradeDetected = false
	previousVersion = ""
	pendingVersionStampPath = ""
	versionBumpFailedThisRun = false
	upgradeAcknowledged = false

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

			trackBdVersion()

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
			settleBdVersionStamp(versionBumpSettled)
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

// TestTrackBdVersion_UpgradeDefersStamp pins the witness lifecycle of
// gastownhall/beads#6929: detecting an upgrade no longer writes .local_version.
// settleBdVersionStamp writes it once the reconciliation the upgrade triggers
// is skipped, settles or is deliberately deferred, and leaves the previous
// version when it failed, so the next invocation detects the upgrade again.
func TestTrackBdVersion_UpgradeDefersStamp(t *testing.T) {
	tests := []struct {
		name      string
		outcome   versionBumpOutcome
		wantStamp bool
	}{
		{name: "skipped", outcome: versionBumpSkipped, wantStamp: true},
		{name: "settled", outcome: versionBumpSettled, wantStamp: true},
		{name: "deferred", outcome: versionBumpDeferred, wantStamp: true},
		{name: "failed", outcome: versionBumpFailed, wantStamp: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localVersionPath := newTrackingWorkspace(t, "1.1.0", "1.3.0")

			trackBdVersion()
			if !versionUpgradeDetected {
				t.Fatal("1.1.0 -> 1.3.0 was not detected as an upgrade")
			}
			if got := readLocalVersion(localVersionPath); got != "1.1.0" {
				t.Fatalf(".local_version = %q right after detection, want the old %q: the upgrade signal was consumed before its reconciliation ran", got, "1.1.0")
			}

			settleBdVersionStamp(tt.outcome)
			want := "1.1.0"
			if tt.wantStamp {
				want = "1.3.0"
			}
			if got := readLocalVersion(localVersionPath); got != want {
				t.Fatalf(".local_version = %q after a %s settlement, want %q", got, tt.name, want)
			}

			// The next invocation: a failed upgrade is detected again, a
			// stamped one is not.
			versionUpgradeDetected, previousVersion = false, ""
			trackBdVersion()
			if versionUpgradeDetected == tt.wantStamp {
				t.Errorf("next invocation versionUpgradeDetected = %v after a %s settlement, want %v",
					versionUpgradeDetected, tt.name, !tt.wantStamp)
			}
		})
	}
}

// TestSettleBdVersionStamp_WritesTheDetectedPath covers doctor's target
// scoping: detection runs with BEADS_DIR pointed at the doctor target and the
// override is restored before the reconciliation settles. The stamp must land
// on the witness detection read, never on whatever BEADS_DIR names by then.
func TestSettleBdVersionStamp_WritesTheDetectedPath(t *testing.T) {
	targetWitness := newTrackingWorkspace(t, "1.1.0", "1.3.0")
	trackBdVersion()

	callerDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(callerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	callerWitness := filepath.Join(callerDir, localVersionFile)
	if err := writeLocalVersion(callerWitness, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", callerDir)

	settleBdVersionStamp(versionBumpSettled)
	if got := readLocalVersion(targetWitness); got != "1.3.0" {
		t.Errorf("target .local_version = %q, want %q", got, "1.3.0")
	}
	if got := readLocalVersion(callerWitness); got != "1.0.0" {
		t.Errorf("caller .local_version = %q, want it untouched at %q", got, "1.0.0")
	}

	// Settlement consumes the pending write: a second one writes nothing.
	if err := writeLocalVersion(targetWitness, "1.1.0"); err != nil {
		t.Fatal(err)
	}
	settleBdVersionStamp(versionBumpSettled)
	if got := readLocalVersion(targetWitness); got != "1.1.0" {
		t.Errorf("a second settlement rewrote .local_version to %q", got)
	}
}

// TestSettleBdVersionStamp_NothingPending covers the paths that must not leave
// a write for settlement: a preview, which detects without consuming, and a
// later detection in the same process that found no upgrade, which clears an
// earlier pending one rather than letting it be settled on its behalf.
func TestSettleBdVersionStamp_NothingPending(t *testing.T) {
	t.Run("preview", func(t *testing.T) {
		localVersionPath := newTrackingWorkspace(t, "1.1.0", "1.3.0")
		trackBdVersionPreview()
		if !versionUpgradeDetected {
			t.Fatal("preview did not detect the upgrade")
		}
		settleBdVersionStamp(versionBumpSettled)
		if got := readLocalVersion(localVersionPath); got != "1.1.0" {
			t.Errorf(".local_version = %q after a preview, want it unconsumed at %q", got, "1.1.0")
		}
	})
	t.Run("repeated detection", func(t *testing.T) {
		firstWitness := newTrackingWorkspace(t, "1.1.0", "1.3.0")
		trackBdVersion()

		secondDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(secondDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(secondDir, "metadata.json"), []byte(`{"database":"beads.db"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeLocalVersion(filepath.Join(secondDir, localVersionFile), "1.3.0"); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", secondDir)
		trackBdVersion()

		settleBdVersionStamp(versionBumpSettled)
		if got := readLocalVersion(firstWitness); got != "1.1.0" {
			t.Errorf("first .local_version = %q, want %q: a stale pending write survived a later detection", got, "1.1.0")
		}
	})
}

// TestAutoMigrateOnVersionBump_Outcomes covers the early returns that need no
// database: those that are skipped under the existing rules, and a config
// that cannot be loaded, which fails and keeps the upgrade for a retry.
func TestAutoMigrateOnVersionBump_Outcomes(t *testing.T) {
	origUpgradeDetected := versionUpgradeDetected
	t.Cleanup(func() { versionUpgradeDetected = origUpgradeDetected })

	versionUpgradeDetected = false
	if got := autoMigrateOnVersionBump(t.TempDir()); got != versionBumpSkipped {
		t.Errorf("no upgrade: outcome = %v, want skipped", got)
	}

	versionUpgradeDetected = true
	if got := autoMigrateOnVersionBump(""); got != versionBumpSkipped {
		t.Errorf("no beads directory: outcome = %v, want skipped", got)
	}
	if got := autoMigrateOnVersionBump(t.TempDir()); got != versionBumpSkipped {
		t.Errorf("no database: outcome = %v, want skipped", got)
	}

	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "metadata.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := autoMigrateOnVersionBump(broken); got != versionBumpFailed {
		t.Errorf("unreadable config: outcome = %v, want failed", got)
	}
}

// TestAutoMigrateOnVersionBump_FailureIsReportedAndRetried drives a failed
// terminal path end to end through the pre-run's own calls: the failure
// reaches stderr with its retry behavior, the previous witness stays, and the
// "bd upgraded" banner is held back for the failed invocation. The next
// invocation, whose upgrade goes through, shows it (gastownhall/beads#6929).
func TestAutoMigrateOnVersionBump_FailureIsReportedAndRetried(t *testing.T) {
	pinJSONOutput(t, false)
	localVersionPath := newTrackingWorkspace(t, "1.1.0", "1.3.0")
	beadsDir := filepath.Dir(localVersionPath)
	metadataPath := filepath.Join(beadsDir, "metadata.json")

	trackBdVersion()
	if err := os.WriteFile(metadataPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	notice := captureNoticeStderr(t, func() {
		settleBdVersionStamp(autoMigrateOnVersionBump(beadsDir))
	})
	for _, want := range []string{"bd upgraded to v1.3.0", "loading config", "retry", "bd doctor"} {
		if !strings.Contains(notice, want) {
			t.Errorf("failure notice is missing %q:\n%s", want, notice)
		}
	}
	if got := readLocalVersion(localVersionPath); got != "1.1.0" {
		t.Errorf(".local_version = %q after a failed upgrade, want the previous %q", got, "1.1.0")
	}
	banner := captureStdout(t, func() error { maybeShowUpgradeNotification(); return nil })
	if banner != "" || upgradeAcknowledged {
		t.Errorf("upgrade banner shown (acknowledged=%v) for an invocation whose upgrade failed:\n%s", upgradeAcknowledged, banner)
	}

	// The next invocation: the config is readable again and there is no
	// database, so the upgrade is skipped, stamped and announced.
	if err := os.WriteFile(metadataPath, []byte(`{"database":"beads.db"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	versionUpgradeDetected, previousVersion = false, ""
	trackBdVersion()
	if !versionUpgradeDetected {
		t.Fatal("the failed upgrade was not detected again on the next invocation")
	}
	notice = captureNoticeStderr(t, func() {
		settleBdVersionStamp(autoMigrateOnVersionBump(beadsDir))
	})
	if notice != "" {
		t.Errorf("a skipped upgrade printed a notice:\n%s", notice)
	}
	if got := readLocalVersion(localVersionPath); got != "1.3.0" {
		t.Errorf(".local_version = %q after the upgrade went through, want %q", got, "1.3.0")
	}
	banner = captureStdout(t, func() error { maybeShowUpgradeNotification(); return nil })
	if !strings.Contains(banner, "bd upgraded from v1.1.0 to v1.3.0") {
		t.Errorf("upgrade banner missing once the upgrade went through:\n%s", banner)
	}
}

// TestNoticeVersionBumpFailure pins the generic failure notice: the underlying
// error, the retry behavior and the diagnostic to run.
func TestNoticeVersionBumpFailure(t *testing.T) {
	pinJSONOutput(t, false)
	out := captureNoticeStderr(t, func() {
		noticeVersionBumpFailure(errors.New("dial tcp 127.0.0.1:1: connection refused"))
	})
	for _, want := range []string{"connection refused", "will retry", "bd doctor"} {
		if !strings.Contains(out, want) {
			t.Errorf("notice is missing %q:\n%s", want, out)
		}
	}
}

// TestVersionBumpOpenFailed classifies the writable open's errors as they
// arrive, wrapped, from the store open: both consent refusals defer the
// upgrade (stamped, so the next command does not repeat the attempt) with
// their existing notices, everything else fails it with the failure notice.
func TestVersionBumpOpenFailed(t *testing.T) {
	pinJSONOutput(t, false)
	tests := []struct {
		name        string
		err         error
		want        versionBumpOutcome
		wantNotice  string
		forbidSubst string
	}{
		{
			name:        "remote-migration gate",
			err:         &schema.RemoteMigrateGateError{CurrentVersion: 72, LatestVersion: 74, Pending: 2, Decision: "shared-no-remote"},
			want:        versionBumpDeferred,
			wantNotice:  "not auto-applying (#5920)",
			forbidSubst: "finishing the upgrade failed",
		},
		{
			name:        "ordinary migration consent",
			err:         &schema.MigrateConsentError{CurrentVersion: 72, LatestVersion: 74, Pending: 2},
			want:        versionBumpDeferred,
			forbidSubst: "finishing the upgrade failed",
		},
		{
			name:       "dirty tables",
			err:        &schema.DirtyTablesError{Tables: []string{"issues"}},
			want:       versionBumpFailed,
			wantNotice: "issues",
		},
		{
			name:       "anything else",
			err:        errors.New("connection refused"),
			want:       versionBumpFailed,
			wantNotice: "connection refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origFailed := versionBumpFailedThisRun
			t.Cleanup(func() { versionBumpFailedThisRun = origFailed })
			wrapped := fmt.Errorf("failed to initialize schema: schema migration: %w", tt.err)
			if got := versionBumpOutcomeForOpenError(wrapped); got != tt.want {
				t.Errorf("versionBumpOutcomeForOpenError = %v, want %v", got, tt.want)
			}
			var got versionBumpOutcome
			out := captureNoticeStderr(t, func() { got = versionBumpOpenFailed(wrapped) })
			if got != tt.want {
				t.Errorf("versionBumpOpenFailed = %v, want %v", got, tt.want)
			}
			if tt.wantNotice == "" && out != "" {
				t.Errorf("expected no notice, got:\n%s", out)
			}
			if tt.wantNotice != "" && !strings.Contains(out, tt.wantNotice) {
				t.Errorf("notice is missing %q:\n%s", tt.wantNotice, out)
			}
			if tt.forbidSubst != "" && strings.Contains(out, tt.forbidSubst) {
				t.Errorf("a deliberate deferral was reported as a failure:\n%s", out)
			}
		})
	}
}

// TestAutoMigrateOnVersionBump_FailureUnderJSON: --json keeps the failure
// notice off stderr, where the command's own machine-readable error may follow,
// while the witness still stays for a retry.
func TestAutoMigrateOnVersionBump_FailureUnderJSON(t *testing.T) {
	pinJSONOutput(t, true)
	localVersionPath := newTrackingWorkspace(t, "1.1.0", "1.3.0")
	beadsDir := filepath.Dir(localVersionPath)

	trackBdVersion()
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var outcome versionBumpOutcome
	out := captureNoticeStderr(t, func() {
		outcome = autoMigrateOnVersionBump(beadsDir)
		settleBdVersionStamp(outcome)
	})
	if outcome != versionBumpFailed {
		t.Fatalf("outcome = %v, want failed", outcome)
	}
	if out != "" {
		t.Errorf("failure notice printed under --json:\n%s", out)
	}
	if got := readLocalVersion(localVersionPath); got != "1.1.0" {
		t.Errorf(".local_version = %q after a failed upgrade under --json, want the previous %q", got, "1.1.0")
	}
}
