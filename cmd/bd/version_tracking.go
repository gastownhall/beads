package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/issueops"
)

// localVersionFile is the gitignored file that stores the last bd version used locally.
// This prevents the upgrade notification from firing repeatedly when git operations
// reset the tracked metadata.json file.
const localVersionFile = ".local_version"

// trackBdVersion checks if bd version has changed since last run and updates the local version file.
// This function is best-effort - failures are silent to avoid disrupting commands.
// Sets global variables versionUpgradeDetected and previousVersion if upgrade detected.
func trackBdVersion() {
	trackBdVersionFile(true)
}

// trackBdVersionPreview does the same detection but leaves .local_version
// alone.
//
// The file is a ONE-SHOT signal: the version-bump reconciliation
// (autoMigrateOnVersionBump, including the pre-0.56 recovery path) fires only
// while the recorded version differs from this binary's. A preview command
// correctly skips that reconciliation — but if it had also rewritten the file,
// it would have consumed the signal on the way past, and the next ordinary
// command would see a matching version and never reconcile at all. Whichever
// command happened to be first after an upgrade would silently decide whether
// the upgrade was ever finished.
//
// Not writing is safe in the other direction too: the marker is advisory and
// the very next non-preview command writes it.
func trackBdVersionPreview() {
	trackBdVersionFile(false)
}

func trackBdVersionFile(persist bool) {
	// Find the beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		// No .beads directory found - this is fine (e.g., bd init, bd version, etc.)
		return
	}

	// Read last version from local (gitignored) file
	localVersionPath := filepath.Join(beadsDir, localVersionFile)
	lastVersion := readLocalVersion(localVersionPath)

	// Check if version changed (only flag actual upgrades, not downgrades)
	if lastVersion != "" && lastVersion != Version {
		if doctor.CompareVersions(Version, lastVersion) > 0 {
			// Version upgrade detected!
			versionUpgradeDetected = true
			previousVersion = lastVersion
		}
	}

	// Update local version file (best effort)
	// Only write if version actually changed to minimize I/O
	if persist && lastVersion != Version {
		_ = writeLocalVersion(localVersionPath, Version) // Best effort: version tracking is advisory
	}

}

// readLocalVersion reads the last bd version from the local version file.
// Returns empty string if file doesn't exist or can't be read.
func readLocalVersion(path string) string {
	// #nosec G304 - path is constructed from beadsDir + constant
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// writeLocalVersion writes the current version to the local version file.
// The value is main.Version verbatim, which release tooling sets by ldflags and
// may be a release candidate, a build carrying metadata, or a Go pseudo-version.
// Readers must accept every shape this can write; see classifyVersionWitness.
func writeLocalVersion(path, version string) error {
	return os.WriteFile(path, []byte(version+"\n"), 0600)
}

// getVersionsSince returns all version changes since the given version.
// If sinceVersion is empty, returns all known versions.
// Returns changes in chronological order (oldest first).
//
// Note: versionChanges array is in reverse chronological order (newest first),
// so we return elements before the found index and reverse the slice.
func getVersionsSince(sinceVersion string) []VersionChange {
	if sinceVersion == "" {
		// Return all versions (already in reverse chronological, but kept for compatibility)
		return versionChanges
	}

	// Find the index of sinceVersion
	// versionChanges is ordered newest-first: [0.23.0, 0.22.1, 0.22.0, 0.21.0]
	startIdx := -1
	for i, vc := range versionChanges {
		if vc.Version == sinceVersion {
			startIdx = i
			break
		}
	}

	if startIdx == -1 {
		// sinceVersion not found in our changelog - return all versions
		// (user might be upgrading from a very old version)
		return versionChanges
	}

	if startIdx == 0 {
		// Already on the newest version
		return []VersionChange{}
	}

	// Return versions before sinceVersion (those are newer)
	// Then reverse to get chronological order (oldest first)
	newerVersions := versionChanges[:startIdx]

	// Reverse the slice to get chronological order
	result := make([]VersionChange, len(newerVersions))
	for i := range newerVersions {
		result[i] = newerVersions[len(newerVersions)-1-i]
	}

	return result
}

// maybeShowUpgradeNotification displays a one-time upgrade notification if version changed.
// This is called by commands like 'bd ready' and 'bd list' to inform users of upgrades.
func maybeShowUpgradeNotification() {
	// Only show if upgrade detected and not yet acknowledged
	if !versionUpgradeDetected || upgradeAcknowledged {
		return
	}

	// Mark as acknowledged so we only show once per session
	upgradeAcknowledged = true

	// Display notification
	fmt.Printf("🔄 bd upgraded from v%s to v%s since last use\n", previousVersion, Version)
	fmt.Println("💡 Run 'bd upgrade review' to see what changed")
	if usesSQLServer() {
		fmt.Println("💊 Run 'bd doctor' to verify upgrade completed cleanly")
	}

	fmt.Println()
}

// autoMigrateOnVersionBump automatically migrates the database when CLI version changes.
// This function is best-effort - failures are silent to avoid disrupting commands.
// Called from PersistentPreRun before opening DB for main operation.
//
// IMPORTANT: This must be called BEFORE opening the database to avoid opening DB twice.
//
// beadsDir is the path to the .beads directory.
func autoMigrateOnVersionBump(beadsDir string) {
	// Only migrate if version upgrade was detected
	if !versionUpgradeDetected {
		return
	}

	// Validate beadsDir
	if beadsDir == "" {
		debug.Logf("auto-migrate: skipping migration, no beads directory")
		return
	}

	// Load config to determine the correct database path for this backend
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		debug.Logf("auto-migrate: failed to load config: %v", err)
		return
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		debug.Logf("auto-migrate: skipping Dolt migration for backend %q", cfg.GetBackend())
		return
	}

	if cfg.IsDoltProxiedServerMode() {
		debug.Logf("auto-migrate: skipping embedded migration, proxied-server handled after UOW provider init")
		return
	}

	// Check if database exists at the backend-appropriate path
	dbPath := cfg.DatabasePath(beadsDir)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// No database - nothing to migrate
		debug.Logf("auto-migrate: skipping migration, database does not exist: %s", dbPath)
		return
	}

	// GH#2137: If upgrading from pre-0.56, the dolt database may have been
	// created by the old embedded Dolt mode. Recover by reinitializing.
	if previousVersion != "" && doctor.CompareVersions(previousVersion, "0.56.0") < 0 {
		recovered, recErr := doltserver.RecoverPreV56DoltDir(dbPath)
		if recErr != nil {
			debug.Logf("auto-migrate: pre-v56 recovery failed: %v", recErr)
		}
		if recovered {
			debug.Logf("auto-migrate: rebuilt pre-v56 dolt database at %s", dbPath)
		}
	}

	// Open database using factory (respects backend config from metadata.json)
	// Use rootCtx if available and not canceled, otherwise use Background
	ctx := rootCtx
	if ctx == nil || ctx.Err() != nil {
		// rootCtx is nil or canceled - use fresh background context
		ctx = context.Background()
	}

	// Read-only probe: if the DB is already at the current version, skip the
	// writeable open. On current main the writeable-open gate reads remotes
	// via doltutil.PersistedRemotes, a fast on-disk repo_state.json probe —
	// not a `dolt remote -v` subprocess — so this doesn't save a 12s hang;
	// it saves an unnecessary initSchema round-trip when no migration is
	// needed. (be-1he)
	//
	// The probe asks the ROLE for the marker rather than reading the metadata
	// key itself: a store opened read-only can answer it.
	if roStore, roErr := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true}); roErr == nil {
		recorded, probeErr := recordedWorkspaceVersion(ctx, roStore)
		_ = roStore.Close()
		if probeErr == nil && recorded == Version {
			debug.Logf("auto-migrate: database already at version %s (ro probe)", Version)
			return
		}
	}

	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		// Failed to open database - skip migration
		debug.Logf("auto-migrate: failed to open database: %v", err)
		return
	}
	defer func() {
		if err := store.Close(); err != nil {
			debug.Logf("auto-migrate: warning: failed to close database: %v", err)
		}
	}()

	// Everything the version markers mean — is this an upgrade, is it a
	// downgrade to refuse, does the high-water mark move — is the role's. This
	// front door reads the outcome and logs it.
	//
	// No Dolt commit is needed after it: local_metadata is dolt-ignored and
	// persists in the working set for the lifetime of the server session.
	reconciler, err := store.VersionReconciler()
	if err != nil {
		debug.Logf("auto-migrate: version markers unavailable: %v", err)
		return
	}
	result, err := reconciler.ReconcileVersion(ctx, issueops.VersionReconcileRequest{CLIVersion: Version})
	if err != nil {
		debug.Logf("auto-migrate: failed to reconcile database version: %v", err)
		return
	}

	switch {
	case result.Downgrade:
		debug.Logf("auto-migrate: refusing downgrade from %s to %s", result.Previous, Version)
	case result.Migrated:
		debug.Logf("auto-migrate: successfully migrated database from %s to version %s", result.Previous, result.Current)
	default:
		debug.Logf("auto-migrate: database already at version %s", Version)
	}
}

// recordedWorkspaceVersion reads the marker through the role's accessor on a
// store the caller owns and closes. It takes the storage interface rather than
// the concrete store so the probe above cannot drift into using a seam the role
// hides.
func recordedWorkspaceVersion(ctx context.Context, store storage.Storage) (string, error) {
	reconciler, err := store.VersionReconciler()
	if err != nil {
		return "", err
	}
	recorded, err := reconciler.RecordedVersion(ctx, issueops.RecordedVersionRequest{})
	if err != nil {
		return "", err
	}
	return recorded.Recorded, nil
}
