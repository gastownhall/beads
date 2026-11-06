//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
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

	// Should NOT have updated .local_version yet: the upgrade's marker write
	// is deferred to the caller and settles only with the version-bump
	// maintenance (#6929). See TestTrackBdVersion_UpgradeDefersMarkerPersist.
	localVersion := readLocalVersion(localVersionPath)
	if localVersion != "0.22.0" {
		t.Errorf(".local_version = %q, want the stale %q kept until maintenance settles", localVersion, "0.22.0")
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
			// An upgrade defers its marker write to the caller (#6929); every
			// other change keeps the immediate write.
			wantMarker := tt.binVersion
			if tt.wantDetected {
				wantMarker = tt.lastVersion
			}
			if got := readLocalVersion(localVersionPath); got != wantMarker {
				t.Errorf(".local_version = %q, want %q", got, wantMarker)
			}
		})
	}
}

// TestTrackBdVersion_UpgradeDefersMarkerPersist pins the #6929 sequencing:
// detecting an upgrade must not itself consume the one-shot .local_version
// signal. The marker may advance only when the caller settles the
// version-bump maintenance; until then the stale marker is what makes the
// next command retry the reconciliation instead of skipping it and hitting
// raw table errors.
func TestTrackBdVersion_UpgradeDefersMarkerPersist(t *testing.T) {
	localVersionPath := newTrackingWorkspace(t, "1.1.0", "1.3.0")

	trackBdVersion()

	if !versionUpgradeDetected {
		t.Fatal("an upgrade must still be detected")
	}
	if previousVersion != "1.1.0" {
		t.Fatalf("previousVersion = %q, want %q", previousVersion, "1.1.0")
	}
	if got := readLocalVersion(localVersionPath); got != "1.1.0" {
		t.Fatalf(".local_version = %q after detection, want the stale %q kept until maintenance settles",
			got, "1.1.0")
	}
}

// newVersionBumpWorkspace prepares the minimal .beads the version-bump
// maintenance needs before its probe/open/reconcile tail: a loadable
// metadata.json on the Dolt backend, a database path that exists, and a stale
// marker. The tail itself is driven by the runVersionBumpReconciliation seam.
func newVersionBumpWorkspace(t *testing.T, lastVersion, binVersion string) (beadsDir, markerPath string) {
	t.Helper()
	markerPath = newTrackingWorkspace(t, lastVersion, binVersion)
	beadsDir = filepath.Dir(markerPath)
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o750); err != nil {
		t.Fatalf("create database dir: %v", err)
	}
	return beadsDir, markerPath
}

// runVersionBumpInvocation replays one ordinary command's pre-run sequence
// against the current workspace: reset the per-process detection globals,
// detect, maintain, and persist the deferred marker only on settlement.
func runVersionBumpInvocation(t *testing.T, beadsDir string) {
	t.Helper()
	versionUpgradeDetected = false
	previousVersion = ""
	pending := trackBdVersion()
	if autoMigrateOnVersionBump(beadsDir) {
		pending.persist()
	}
}

// stubVersionBumpTail replaces the probe/open/reconcile seam for this test.
// The stub receives the 1-based attempt number and reports the tail's
// outcome; the installed counter starts at zero. Tests using it must not run
// in parallel; the original is restored in cleanup.
func stubVersionBumpTail(t *testing.T, stub func(attempt int) (settled bool, openErr error)) {
	t.Helper()
	orig := runVersionBumpReconciliation
	t.Cleanup(func() { runVersionBumpReconciliation = orig })
	versionBumpTailAttempts = 0
	runVersionBumpReconciliation = func(ctx context.Context, beadsDir string) (bool, error) {
		versionBumpTailAttempts++
		return stub(versionBumpTailAttempts)
	}
}

// versionBumpTailAttempts counts the attempts of the currently installed
// stubVersionBumpTail override. A package-level counter keeps the stub
// signature about outcomes only.
var versionBumpTailAttempts int

// TestVersionBump_DirtyTablesKeepsMarkerAndRetries proves the #6929 behavior
// across invocations, not just classification: while the version-bump
// reconciliation is refused, each command re-detects the upgrade, retries the
// maintenance and keeps the stale marker; the first success advances the
// marker, and the next command attempts no maintenance at all.
func TestVersionBump_DirtyTablesKeepsMarkerAndRetries(t *testing.T) {
	beadsDir, markerPath := newVersionBumpWorkspace(t, "1.1.0", "1.3.0")

	stubVersionBumpTail(t, func(attempt int) (bool, error) {
		if attempt <= 2 {
			// Wrapped the way a real open failure arrives.
			return false, fmt.Errorf("open %s: %w", beadsDir,
				&schema.DirtyTablesError{Tables: []string{"issues", "wisps"}})
		}
		return true, nil
	})

	// The refused invocations report the commit-then-migrate remedy on stderr
	// (captured here both to assert it and to keep the refusal's notice out of
	// the test's own output).
	refused := captureNoticeStderr(t, func() { runVersionBumpInvocation(t, beadsDir) })
	refused += captureNoticeStderr(t, func() { runVersionBumpInvocation(t, beadsDir) })
	if got := readLocalVersion(markerPath); got != "1.1.0" {
		t.Fatalf("after two refused invocations: .local_version = %q, want the stale %q so each command retries", got, "1.1.0")
	}
	if !strings.Contains(refused, "bd dolt commit") {
		t.Errorf("the refused version-bump maintenance did not report its recovery path on stderr:\n%s", refused)
	}
	if versionBumpTailAttempts != 2 {
		t.Fatalf("maintenance attempts after two commands = %d, want 2 (one retry per command)", versionBumpTailAttempts)
	}

	runVersionBumpInvocation(t, beadsDir)
	if got := readLocalVersion(markerPath); got != "1.3.0" {
		t.Fatalf("after the successful invocation: .local_version = %q, want %q", got, "1.3.0")
	}

	runVersionBumpInvocation(t, beadsDir)
	if versionBumpTailAttempts != 3 {
		t.Fatalf("maintenance attempts after a settled bump = %d, want 3: a settled marker must not re-attempt maintenance", versionBumpTailAttempts)
	}
}

// TestVersionBump_PerformedPreV56RecoveryConsumesOneShot pins the #2137
// legacy exception to #6929's retry semantics: the pre-0.56 recovery DELETES
// .dolt and rebuilds it, and the compatibility marker it leaves is
// best-effort — a retained pre-0.56 witness could route the next command
// into the same destructive work again. A performed recovery therefore
// consumes the one-shot bump even when the reconciliation that follows
// fails.
func TestVersionBump_PerformedPreV56RecoveryConsumesOneShot(t *testing.T) {
	beadsDir, markerPath := newVersionBumpWorkspace(t, "0.55.4", "1.3.0")

	// The pre-v56 shape recovery destroys: a .dolt with no .bd-dolt-ok
	// marker. The sentinel's disappearance is the proof the recovery ran.
	sentinel := filepath.Join(beadsDir, "dolt", ".dolt", "sentinel.txt")
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sentinel, []byte("live workspace data"), 0o600); err != nil {
		t.Fatal(err)
	}

	stubVersionBumpTail(t, func(int) (bool, error) {
		return false, fmt.Errorf("open: %w", &schema.DirtyTablesError{Tables: []string{"issues"}})
	})

	_ = captureNoticeStderr(t, func() { runVersionBumpInvocation(t, beadsDir) })

	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("the pre-v56 recovery did not run: %s survived", sentinel)
	}
	if got := readLocalVersion(markerPath); got != "1.3.0" {
		t.Fatalf(".local_version = %q after a performed pre-v56 recovery, want %q — destructive recovery consumes the one-shot even when the tail fails",
			got, "1.3.0")
	}
}

// TestVersionBump_NotApplicablePathsSettleWithoutReconciliation pins that the
// not-applicable paths — where version-bump maintenance has nothing to do —
// settle the bump without opening a store, and the deferred marker advances.
func TestVersionBump_NotApplicablePathsSettleWithoutReconciliation(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setUp func(t *testing.T, beadsDir string)
	}{
		{name: "non-dolt backend", setUp: func(t *testing.T, beadsDir string) {
			overwriteVersionBumpMetadata(t, beadsDir, &configfile.Config{Backend: configfile.BackendPostgres})
		}},
		{name: "proxied-server mode", setUp: func(t *testing.T, beadsDir string) {
			overwriteVersionBumpMetadata(t, beadsDir, &configfile.Config{DoltMode: configfile.DoltModeProxiedServer})
		}},
		{name: "missing database", setUp: func(t *testing.T, beadsDir string) {
			if err := os.RemoveAll(filepath.Join(beadsDir, "dolt")); err != nil {
				t.Fatalf("remove database dir: %v", err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir, markerPath := newVersionBumpWorkspace(t, "1.1.0", "1.3.0")
			tt.setUp(t, beadsDir)
			// Any tail call fails the test: these paths must settle without
			// opening a store.
			stubVersionBumpTail(t, func(int) (bool, error) {
				t.Error("the probe/open/reconcile tail must not run on a not-applicable path")
				return false, nil
			})

			runVersionBumpInvocation(t, beadsDir)

			if got := readLocalVersion(markerPath); got != "1.3.0" {
				t.Fatalf(".local_version = %q after a not-applicable invocation, want %q", got, "1.3.0")
			}
		})
	}
}

// overwriteVersionBumpMetadata replaces the workspace's metadata.json for the
// not-applicable rows.
func overwriteVersionBumpMetadata(t *testing.T, beadsDir string, cfg *configfile.Config) {
	t.Helper()
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
}

// TestVersionBump_ReconcileOutcomeSettles pins the writable-maintenance half
// of AC-4: a completed reconciliation — migrated, already-current, or a
// refused downgrade, each the tail's own settled outcome — advances the
// deferred marker through the production sequence.
func TestVersionBump_ReconcileOutcomeSettles(t *testing.T) {
	beadsDir, markerPath := newVersionBumpWorkspace(t, "1.1.0", "1.3.0")
	stubVersionBumpTail(t, func(int) (bool, error) { return true, nil })

	runVersionBumpInvocation(t, beadsDir)

	if got := readLocalVersion(markerPath); got != "1.3.0" {
		t.Fatalf(".local_version = %q after a completed reconciliation, want %q", got, "1.3.0")
	}
}

// TestTrackBdVersionPreview_NeverYieldsAPendingWrite pins the preview half of
// AC-4: preview policy excludes the maintenance, so it must also exclude the
// marker write entirely — no immediate write and no pending write a later
// step could persist. Readonly and freeze policy exclude both calls the same
// way before this point (see TestAutoMigrateSkippedDuringMigrationFreeze).
func TestTrackBdVersionPreview_NeverYieldsAPendingWrite(t *testing.T) {
	localVersionPath := newTrackingWorkspace(t, "1.1.0", "1.3.0")

	trackBdVersionPreview()

	if !versionUpgradeDetected {
		t.Fatal("a preview must still detect the upgrade (the session notification reads it)")
	}
	if got := readLocalVersion(localVersionPath); got != "1.1.0" {
		t.Fatalf(".local_version = %q after a preview, want the stale %q", got, "1.1.0")
	}
}

// newSecondaryTrackingWorkspace builds a bare .beads workspace (metadata,
// database dir, marker) without touching BEADS_DIR, the cwd or the pinned
// version globals — for tests that need a second workspace beside the one
// newTrackingWorkspace prepared.
func newSecondaryTrackingWorkspace(t *testing.T, lastVersion string) (beadsDir, markerPath string) {
	t.Helper()
	tmpDir := t.TempDir()
	beadsDir = filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"database":"beads.db"}`), 0o600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o750); err != nil {
		t.Fatalf("create database dir: %v", err)
	}
	markerPath = filepath.Join(beadsDir, localVersionFile)
	if err := writeLocalVersion(markerPath, lastVersion); err != nil {
		t.Fatalf("write %s: %v", localVersionFile, err)
	}
	return beadsDir, markerPath
}

// TestDoctorVersionMaintenance_TargetsOnlyTheDoctorWorkspace walks AC-5:
// `bd doctor <path>` detects, reconciles and persists against the TARGET
// workspace only. Its pending marker captures the path resolved while
// BEADS_DIR pointed at the target, so restoring the caller's environment
// cannot redirect the write; the caller's own (equally stale) marker must
// not move, and BEADS_DIR must come back exactly as it was — set or unset.
func TestDoctorVersionMaintenance_TargetsOnlyTheDoctorWorkspace(t *testing.T) {
	for _, tt := range []struct {
		name             string
		settle           bool
		refusal          error
		wantTargetMarker string
	}{
		{
			name:             "ordinary failure retains the target marker",
			settle:           false,
			refusal:          fmt.Errorf("open: %w", &schema.DirtyTablesError{Tables: []string{"issues"}}),
			wantTargetMarker: "1.1.0",
		},
		{
			name:             "settlement advances the target marker",
			settle:           true,
			wantTargetMarker: "1.3.0",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, initial := range []struct {
				name string
				set  bool
			}{
				{name: "beads_dir initially set to the caller", set: true},
				{name: "beads_dir initially unset", set: false},
			} {
				t.Run(initial.name, func(t *testing.T) {
					// The doctor target: prepared through the standard helper
					// so the pinned version globals and BEADS_DIR match its
					// stale marker.
					targetBeadsDir, targetMarker := newVersionBumpWorkspace(t, "1.1.0", "1.3.0")
					// The caller's own workspace, with a stale marker of its
					// own: any leak of tracking or persistence to the caller
					// would rewrite it.
					callerBeadsDir, callerMarker := newSecondaryTrackingWorkspace(t, "1.0.0")
					if initial.set {
						t.Setenv("BEADS_DIR", callerBeadsDir)
					} else {
						os.Unsetenv("BEADS_DIR")
					}

					stubVersionBumpTail(t, func(int) (bool, error) { return tt.settle, tt.refusal })

					_ = captureNoticeStderr(t, func() { runDoctorVersionMaintenance(targetBeadsDir) })

					if got := readLocalVersion(targetMarker); got != tt.wantTargetMarker {
						t.Errorf("target .local_version = %q, want %q", got, tt.wantTargetMarker)
					}
					if got := readLocalVersion(callerMarker); got != "1.0.0" {
						t.Errorf("caller .local_version = %q, want its stale %q untouched — doctor's maintenance leaked to the caller's workspace",
							got, "1.0.0")
					}
					if versionBumpTailAttempts != 1 {
						t.Fatalf("maintenance attempts = %d, want exactly one for the target", versionBumpTailAttempts)
					}
					if got, had := os.LookupEnv("BEADS_DIR"); initial.set != had || (had && got != callerBeadsDir) {
						t.Errorf("BEADS_DIR after doctor maintenance = (%q, %t), want the caller's (%q, true)",
							got, had, callerBeadsDir)
					}
				})
			}
		})
	}
}

// TestVersionBump_DeferralsSettle pins that a writable-open refusal which IS
// the operator's decision — the remote-migrate gate or the migration-consent
// refusal — settles the one-shot bump instead of retrying: the remedy
// commands open the store on their own, so re-opening and re-noticing on
// every following command would only nag about a decision only the operator
// can change. The gate's per-decision notice must keep firing exactly once.
func TestVersionBump_DeferralsSettle(t *testing.T) {
	for _, tt := range []struct {
		name       string
		err        error
		wantNotice string
	}{
		{
			name:       "remote gate refusal",
			err:        &schema.RemoteMigrateGateError{CurrentVersion: 72, LatestVersion: 74, Pending: 2, Decision: "shared-no-remote"},
			wantNotice: "not auto-applying",
		},
		{
			name: "wrapped gate refusal",
			err: fmt.Errorf("open store: %w", &schema.RemoteMigrateGateError{
				CurrentVersion: 72, LatestVersion: 74, Pending: 1, Decision: "adopt",
			}),
			wantNotice: "bd bootstrap",
		},
		{
			name: "migration consent refusal",
			err:  &schema.MigrateConsentError{CurrentVersion: 72, LatestVersion: 74, Pending: 2},
			// No pre-run notice for this refusal: the command's own open
			// reports it with its full options block.
			wantNotice: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir, markerPath := newVersionBumpWorkspace(t, "1.1.0", "1.3.0")
			stubVersionBumpTail(t, func(int) (bool, error) { return false, tt.err })

			first := captureNoticeStderr(t, func() { runVersionBumpInvocation(t, beadsDir) })
			if got := readLocalVersion(markerPath); got != "1.3.0" {
				t.Fatalf("after the deferred invocation: .local_version = %q, want %q — a deliberate deferral settles the bump", got, "1.3.0")
			}
			if tt.wantNotice != "" && !strings.Contains(first, tt.wantNotice) {
				t.Errorf("the per-decision gate diagnostic changed or went missing:\n%s", first)
			}
			if tt.wantNotice == "" && first != "" {
				t.Errorf("this deferral must rely on the command's own diagnostics, got a pre-run notice:\n%s", first)
			}

			// A settled bump makes the second invocation attempt nothing and
			// notice nothing.
			second := captureNoticeStderr(t, func() { runVersionBumpInvocation(t, beadsDir) })
			if versionBumpTailAttempts != 1 {
				t.Fatalf("maintenance attempts after a settled deferral = %d, want 1", versionBumpTailAttempts)
			}
			if strings.Contains(second, "not auto-applying") {
				t.Errorf("the gate notice repeated after the bump settled:\n%s", second)
			}
		})
	}
}

// TestNoticeDirtyTablesRefusal covers the human recovery path for a
// dirty-table stop of the version-bump reconciliation (#6929): name the dirty
// tables, the workspace whose working set must be committed, and the
// commit-then-migrate remedy — and stay silent under --json, where prose on
// stderr would sit in front of the command's machine-readable error.
func TestNoticeDirtyTablesRefusal(t *testing.T) {
	// The notice self-suppresses under --json; pin the mode like the gate
	// notice's test does, so a leak from an earlier test cannot read as
	// silence here.
	pinJSONOutput(t, false)

	wrapped := fmt.Errorf("open %s: %w", "/ws/.beads",
		&schema.DirtyTablesError{Tables: []string{"issues", "wisps"}})

	t.Run("human output names tables, workspace and remedy", func(t *testing.T) {
		out := captureNoticeStderr(t, func() { noticeDirtyTablesRefusal(wrapped, "/ws/.beads") })
		for _, want := range []string{"issues", "wisps", "/ws/.beads", "bd dolt commit", "bd migrate schema"} {
			if !strings.Contains(out, want) {
				t.Errorf("notice missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("silent in json mode", func(t *testing.T) {
		pinJSONOutput(t, true)
		out := captureNoticeStderr(t, func() { noticeDirtyTablesRefusal(wrapped, "/ws/.beads") })
		if out != "" {
			t.Errorf("notice must be silent in --json mode, got:\n%s", out)
		}
	})

	t.Run("untyped error prints nothing", func(t *testing.T) {
		out := captureNoticeStderr(t, func() { noticeDirtyTablesRefusal(errors.New("connection refused"), "/ws/.beads") })
		if out != "" {
			t.Errorf("only a dirty-table refusal should produce a notice, got:\n%s", out)
		}
	})

	// The refusal targets the maintenance workspace's own open, which predates
	// the command's --global database selection; the remedy must not be
	// rewritten with --global (it could repair a database this refusal did
	// not name) and must keep naming the workspace instead.
	t.Run("no global rewrite under the global flag", func(t *testing.T) {
		orig := globalFlag
		globalFlag = true
		defer func() { globalFlag = orig }()
		out := captureNoticeStderr(t, func() { noticeDirtyTablesRefusal(wrapped, "/ws/.beads") })
		if strings.Contains(out, "--global") {
			t.Errorf("notice must not append --global from the command flag:\n%s", out)
		}
		if !strings.Contains(out, "/ws/.beads") {
			t.Errorf("notice must name the maintenance workspace:\n%s", out)
		}
	})
}

// TestVersionBump_OrdinaryFailuresRetry pins that every ordinary failure —
// not just the dirty-table refusal — keeps the one-shot signal for the next
// command. Rows cover the writable open failing without a typed refusal, the
// reconcile step failing after a successful open, and a workspace whose
// metadata cannot even be loaded (no attempt is possible, none may count as
// settled).
func TestVersionBump_OrdinaryFailuresRetry(t *testing.T) {
	for _, tt := range []struct {
		name            string
		corruptMetadata bool
		stub            func(attempt int) (bool, error)
		wantAttempts    int
	}{
		{
			name:         "generic writable-open failure",
			stub:         func(int) (bool, error) { return false, errors.New("dial tcp 127.0.0.1:3307: connection refused") },
			wantAttempts: 2,
		},
		{
			name:         "reconcile failure after a successful open",
			stub:         func(int) (bool, error) { return false, nil },
			wantAttempts: 2,
		},
		{
			name:            "malformed metadata keeps the signal",
			corruptMetadata: true,
			stub:            func(int) (bool, error) { return true, nil },
			wantAttempts:    0,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir, markerPath := newVersionBumpWorkspace(t, "1.1.0", "1.3.0")
			if tt.corruptMetadata {
				if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("not json"), 0o600); err != nil {
					t.Fatalf("corrupt metadata.json: %v", err)
				}
			}
			stubVersionBumpTail(t, tt.stub)

			runVersionBumpInvocation(t, beadsDir)
			runVersionBumpInvocation(t, beadsDir)

			if got := readLocalVersion(markerPath); got != "1.1.0" {
				t.Fatalf(".local_version = %q after failing invocations, want the stale %q", got, "1.1.0")
			}
			if versionBumpTailAttempts != tt.wantAttempts {
				t.Fatalf("maintenance attempts = %d, want %d", versionBumpTailAttempts, tt.wantAttempts)
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
