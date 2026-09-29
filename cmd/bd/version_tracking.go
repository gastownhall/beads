package main

import (
	"context"
	"errors"
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
	"github.com/steveyegge/beads/internal/storage/schema"
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

	// Check if version changed (only flag actual upgrades, not downgrades).
	//
	// Homebrew --HEAD stamps (HEAD-<sha>) carry no ordering, so CompareVersions
	// reads them as 0.0.0: a reinstall onto a newer HEAD registers as no change
	// at all, and moving a release workspace onto HEAD registers as a
	// downgrade. Treat a changed stamp with a HEAD build on either side as an
	// upgrade, so the one-shot post-upgrade reconciliation actually runs; brew
	// reinstalls only move forward. If that assumption is ever wrong the
	// misfire is bounded — recoverPreV56IfNeeded refuses a non-semver
	// predecessor, and the workspace's own version markers still refuse a
	// genuine downgrade. (Those markers cannot arbitrate direction here: they
	// compare with the same dotted scan and read HEAD stamps as 0.0.0 too.)
	if lastVersion != "" && lastVersion != Version {
		if doctor.CompareVersions(Version, lastVersion) > 0 ||
			doctor.IsBrewHeadVersion(Version) || doctor.IsBrewHeadVersion(lastVersion) {
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

	// A brew --HEAD stamp names no changelog entry; without this the
	// unknown-version fallback below would report the entire release history
	// as new after every --HEAD reinstall.
	if doctor.IsBrewHeadVersion(sinceVersion) {
		return []VersionChange{}
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

// displayVersion formats a version stamp for user-facing messages: semver
// stamps get the conventional "v" prefix, while a brew --HEAD stamp is shown
// verbatim ("vHEAD-f925f3f" reads as a typo).
func displayVersion(version string) string {
	if doctor.IsBrewHeadVersion(version) {
		return version
	}
	return "v" + version
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
	fmt.Printf("🔄 bd upgraded from %s to %s since last use\n", displayVersion(previousVersion), displayVersion(Version))
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

	recoverPreV56IfNeeded(previousVersion, dbPath)

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
		noticeSharedMigrateRefusal(err)
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

// recoverPreV56IfNeeded rebuilds a Dolt database left behind by the pre-0.56
// embedded mode (GH#2137) — which means deleting .dolt — but only when the
// recorded predecessor is an actual semantic version older than 0.56.0.
//
// The validity check is not redundant with the comparison. CompareVersions
// scans every dot-separated part with %d and leaves whatever it cannot read at
// 0, so any non-semver string writeLocalVersion has recorded compares as
// pre-0.56 and routes a current workspace into this destructive path:
// Homebrew's "HEAD-<shortsha>" from a --HEAD install (#5603), and the
// v-prefixed Go pseudo-version a `go install`-built bd stamps (#5650). Neither
// is a pre-0.56 workspace; both would lose their database.
//
// IsValidSemver rejects exactly the shapes CompareVersions misreads, so this
// gate can only remove predecessors from the recovery set, never add one: a
// stamp that reaches RecoverPreV56DoltDir today and still parses keeps its
// recovery unchanged.
func recoverPreV56IfNeeded(previousVersion, dbPath string) {
	if !doctor.IsValidSemver(previousVersion) {
		return
	}
	if doctor.CompareVersions(previousVersion, "0.56.0") >= 0 {
		return
	}

	recovered, recErr := doltserver.RecoverPreV56DoltDir(dbPath)
	if recErr != nil {
		debug.Logf("auto-migrate: pre-v56 recovery failed: %v", recErr)
	}
	if recovered {
		debug.Logf("auto-migrate: rebuilt pre-v56 dolt database at %s", dbPath)
	}
}

// noticeSharedMigrateRefusal turns the one failure autoMigrateOnVersionBump
// must not swallow into a one-line stderr notice.
//
// Every other failure here is genuinely best-effort — the command's own store
// open will report anything that matters. A gate refusal is different: it is
// the whole point of the version bump (there ARE pending migrations), it will
// not be reported by a read command's own open (read-only opens never touch
// the schema), and it is the moment gastownhall/beads#5920 used to silently
// promote the cursor for every co-resident client. Now nothing is promoted, so
// say what happened and what to do about it.
//
// It fires once per version bump, because .local_version is consumed in the
// same pre-run — which is exactly why the remedy it names has to be right for
// THIS refusal. The gate returns one error type for several very different
// decisions, and the remote-backed ones are not unlocked by the migrate verb
// at all (a clone whose remote is already migrated must adopt, not migrate),
// so the line is chosen per decision rather than printed unconditionally.
func noticeSharedMigrateRefusal(err error) {
	var gateErr *schema.RemoteMigrateGateError
	if !errors.As(err, &gateErr) {
		return
	}
	// --json puts a machine-readable gate block on this same stream when the
	// command's own open is refused a moment later; prose prepended to it
	// makes the documented contract unparseable. The human-output sibling
	// maybeShowUpgradeNotification takes the same care.
	if jsonOutput {
		return
	}

	consent := schema.SharedConsentCommand
	if globalFlag {
		consent = schema.SharedConsentCommandGlobal
	}
	var remedy string
	switch gateErr.Decision {
	case "shared-no-remote":
		remedy = fmt.Sprintf("Run '%s' once every client of this server is upgraded; reads keep working meanwhile.", consent)
	case "adopt", "adopt-ff":
		// The remote is already migrated: migrating here would fork it, and
		// the verb consent deliberately does not unlock this arm.
		remedy = "Another clone has already migrated this database — adopt it with 'bd bootstrap' rather than migrating here; reads keep working meanwhile."
	case "fork-skew":
		remedy = "This database and its remote already applied different content for the same migration (#4259) — run 'bd doctor' and follow its migration-content-skew guidance; do not migrate."
	default:
		// The blunt remote-backed stop, including the shared-store
		// first-mover suppression. Its full block names the designated-
		// migrator and adopt paths with their preconditions, and picking one
		// here would be guessing.
		remedy = "This database has a remote — run 'bd migrate' to see the migrate-or-adopt options before choosing; reads keep working meanwhile."
	}
	fmt.Fprintf(os.Stderr,
		"bd upgraded to %s: %d schema migration(s) pending on this database — not auto-applying (#5920).\n%s\n",
		displayVersion(Version), gateErr.Pending, remedy)
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
