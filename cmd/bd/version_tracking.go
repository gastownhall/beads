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
	"golang.org/x/mod/module"
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

// pendingVersionStampPath is the resolved .local_version path whose write the
// most recent upgrade detection deferred. Writing the witness at detection
// made the one-shot signal single-shot in the wrong direction too: a
// reconciliation that failed — most reportably schema.DirtyTablesError,
// refused because the working set was dirty — could never be retried, because
// no later command saw an upgrade anymore (gastownhall/beads#6929). The write
// now happens at settlement (settleBdVersionStamp), after
// autoMigrateOnVersionBump reports its outcome. Empty when nothing is
// pending; cleared at detection entry and by settlement, and reset alongside
// the other tracking globals in tests.
var pendingVersionStampPath string

func trackBdVersionFile(persist bool) {
	// Detection entry: whatever an earlier call in this process deferred is no
	// longer pending — the file on disk is the truth this call re-reads.
	pendingVersionStampPath = ""

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
	//
	// upgraded is this call's own local comparison, deliberately not the
	// sticky versionUpgradeDetected flag: the flag survives this function, the
	// write decision must not depend on state from an earlier detection.
	upgraded := false
	if lastVersion != "" && lastVersion != Version {
		if doctor.CompareVersions(Version, lastVersion) > 0 ||
			doctor.IsBrewHeadVersion(Version) || doctor.IsBrewHeadVersion(lastVersion) {
			// Version upgrade detected!
			upgraded = true
			versionUpgradeDetected = true
			previousVersion = lastVersion
		}
	}

	// The witness write, by shape:
	//   - an upgrade (persisting callers only): deferred to settlement, so a
	//     failed reconciliation leaves the signal for the next command;
	//   - a first run or a downgrade (persisting callers only): written now,
	//     as before — neither shape triggers a reconciliation;
	//   - a preview or an unchanged version: no write. A preview must never
	//     create a pending write either, for the same one-shot reason it must
	//     not write the file (see trackBdVersionPreview).
	switch {
	case !persist:
	case upgraded:
		pendingVersionStampPath = localVersionPath
	case lastVersion != Version:
		_ = writeLocalVersion(localVersionPath, Version) // Best effort: version tracking is advisory
	}
}

// settleBdVersionStamp completes the witness write an upgrade detection
// deferred once the version-bump reconciliation reported its outcome.
// skipped, settled and deferred all consumed the one-shot upgrade signal
// honestly — the reconciliation was not eligible, completed (including by
// refusing a downgrade), or was deliberately not consented to — so the exact
// witness path read during detection is stamped with the running version.
// failed leaves the previous witness in place: the next eligible command
// re-detects the upgrade and retries the reconciliation. The pending path is
// cleared either way, and the write stays best effort, as it always was at
// detection time.
//
// Settlement writes the CAPTURED path, not a re-resolved one, which is what
// keeps `bd doctor <path>` scoped to its target: detection ran with
// BEADS_DIR pointing there, and a settle after the caller restored its own
// BEADS_DIR still lands in the target workspace.
func settleBdVersionStamp(outcome versionBumpOutcome) {
	path := pendingVersionStampPath
	pendingVersionStampPath = ""
	if path == "" || outcome == bumpFailed {
		return
	}
	_ = writeLocalVersion(path, Version)
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

// versionBumpFailed records that this invocation's version-bump
// reconciliation failed (autoMigrateOnVersionBump). It suppresses the upgrade
// banner in maybeShowUpgradeNotification: "bd upgraded" would contradict the
// failure notice the operator just read, and acknowledging the banner now
// would keep it from appearing on the next, successful command.
var versionBumpFailed bool

// maybeShowUpgradeNotification displays a one-time upgrade notification if version changed.
// This is called by commands like 'bd ready' and 'bd list' to inform users of upgrades.
func maybeShowUpgradeNotification() {
	// Only show if upgrade detected and not yet acknowledged
	if !versionUpgradeDetected || upgradeAcknowledged {
		return
	}
	// The upgrade's own reconciliation failed this invocation (#6929): the
	// database is still behind and the failure notice just said so. Show the
	// banner on a later command instead — without acknowledging here — by
	// which point the retry has either settled or the operator intervened.
	if versionBumpFailed {
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

// versionBumpOutcome is what autoMigrateOnVersionBump did with the upgrade
// trackBdVersion detected, and therefore what settleBdVersionStamp may do
// with the deferred witness write: every outcome except bumpFailed consumed
// the one-shot upgrade signal honestly, and only a failure must leave it for
// the next eligible command to retry with.
type versionBumpOutcome int

const (
	// bumpSkipped: no reconciliation was eligible — no upgrade detected, no
	// beads directory, a non-Dolt backend, a proxied-server workspace, or no
	// local database to migrate.
	bumpSkipped versionBumpOutcome = iota
	// bumpSettled: the read-only probe found the database already at this
	// binary's version, or the reconciliation ran to a conclusion, including
	// its explicit downgrade refusal.
	bumpSettled
	// bumpDeferred: the operator did not consent — the #5920 remote-migration
	// gate or the ordinary migration-consent refusal (schema.MigrateConsentError).
	// A deliberate deferral, not a failure.
	bumpDeferred
	// bumpFailed: the reconciliation wanted to finish the upgrade and could
	// not — config load, writable open, reconciler acquisition, or the
	// reconciliation itself failed for a non-consent reason.
	bumpFailed
)

// autoMigrateOnVersionBump automatically migrates the database when CLI version changes.
// This function is best-effort - failures do not fail the command, whose own
// store open and query determine its exit status; they are reported (see
// noticeVersionBumpFailure) and reflected in the returned outcome instead.
// Called from PersistentPreRun before opening DB for main operation.
//
// IMPORTANT: This must be called BEFORE opening the database to avoid opening DB twice.
//
// The returned outcome settles the witness write the upgrade detection
// deferred (settleBdVersionStamp): callers wrap the call as
// settleBdVersionStamp(autoMigrateOnVersionBump(beadsDir)).
//
// beadsDir is the path to the .beads directory.
func autoMigrateOnVersionBump(beadsDir string) versionBumpOutcome {
	outcome := runVersionBumpMigration(beadsDir)
	versionBumpFailed = outcome == bumpFailed
	return outcome
}

// runVersionBumpMigration is autoMigrateOnVersionBump's body; the wrapper
// records the outcome so maybeShowUpgradeNotification can react to a failed
// bump without every call site changing.
func runVersionBumpMigration(beadsDir string) versionBumpOutcome {
	// Only migrate if version upgrade was detected
	if !versionUpgradeDetected {
		return bumpSkipped
	}

	// Validate beadsDir
	if beadsDir == "" {
		debug.Logf("auto-migrate: skipping migration, no beads directory")
		return bumpSkipped
	}

	// Load config to determine the correct database path for this backend
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		debug.Logf("auto-migrate: failed to load config: %v", err)
		noticeVersionBumpFailure(err)
		return bumpFailed
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	if cfg.GetBackend() != configfile.BackendDolt {
		debug.Logf("auto-migrate: skipping Dolt migration for backend %q", cfg.GetBackend())
		return bumpSkipped
	}

	if cfg.IsDoltProxiedServerMode() {
		debug.Logf("auto-migrate: skipping embedded migration, proxied-server handled after UOW provider init")
		return bumpSkipped
	}

	// Check if database exists at the backend-appropriate path
	dbPath := cfg.DatabasePath(beadsDir)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// No database - nothing to migrate
		debug.Logf("auto-migrate: skipping migration, database does not exist: %s", dbPath)
		return bumpSkipped
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
	// key itself: a store opened read-only can answer it. A probe that fails
	// decides nothing on its own — it falls through to the writable attempt,
	// whose outcome is the one that settles.
	if roStore, roErr := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true}); roErr == nil {
		recorded, probeErr := recordedWorkspaceVersion(ctx, roStore)
		_ = roStore.Close()
		if probeErr == nil && recorded == Version {
			debug.Logf("auto-migrate: database already at version %s (ro probe)", Version)
			return bumpSettled
		}
	}

	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		// Failed to open database - skip migration
		debug.Logf("auto-migrate: failed to open database: %v", err)
		return reportVersionBumpRefusal(err)
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
		noticeVersionBumpFailure(err)
		return bumpFailed
	}
	result, err := reconciler.ReconcileVersion(ctx, issueops.VersionReconcileRequest{CLIVersion: Version})
	if err != nil {
		debug.Logf("auto-migrate: failed to reconcile database version: %v", err)
		return reportVersionBumpRefusal(err)
	}

	switch {
	case result.Downgrade:
		debug.Logf("auto-migrate: refusing downgrade from %s to %s", result.Previous, Version)
	case result.Migrated:
		debug.Logf("auto-migrate: successfully migrated database from %s to version %s", result.Previous, result.Current)
	default:
		debug.Logf("auto-migrate: database already at version %s", Version)
	}
	return bumpSettled
}

// classifyVersionBumpError maps a terminal error from the version-bump
// reconciliation to its outcome. The two consent decisions — the #5920
// remote-migration gate and the ordinary migration-consent refusal — are
// deliberate deferrals: the operator (or the absence of their consent) chose
// not to migrate now, so the upgrade witness settles and the existing
// per-decision notice behavior is kept. Anything else failed.
func classifyVersionBumpError(err error) versionBumpOutcome {
	var gateErr *schema.RemoteMigrateGateError
	var consentErr *schema.MigrateConsentError
	if errors.As(err, &gateErr) || errors.As(err, &consentErr) {
		return bumpDeferred
	}
	return bumpFailed
}

// reportVersionBumpRefusal reports a terminal error from the reconciliation's
// writable path under the notice its classification calls for — the gate's
// per-decision line for a consent deferral, the failure notice for anything
// else — and returns the outcome settleBdVersionStamp needs.
func reportVersionBumpRefusal(err error) versionBumpOutcome {
	outcome := classifyVersionBumpError(err)
	if outcome == bumpDeferred {
		// noticeSharedMigrateRefusal prints the gate's own per-decision
		// remedy; an ordinary MigrateConsentError has no bump-path notice
		// today and keeps that silence.
		noticeSharedMigrateRefusal(err)
	} else {
		noticeVersionBumpFailure(err)
	}
	return outcome
}

// noticeVersionBumpFailure is the sibling failure notice for a version-bump
// reconciliation that ended without either consent decision. The gate refusal
// (noticeSharedMigrateRefusal) is the operator declining to migrate; this is
// the upgrade's own reconciliation wanting to finish and failing — most
// reportably schema.DirtyTablesError, refused because the working set has
// uncommitted changes in a table a pending migration alters. Because the
// .local_version signal is NOT consumed on failure (settleBdVersionStamp),
// the next eligible non-preview command retries the attempt; the notice says
// so and names the remedy.
//
// --json keeps the same silence noticeSharedMigrateRefusal keeps: prose on
// this stream breaks the machine-readable output contract.
func noticeVersionBumpFailure(err error) {
	if jsonOutput {
		return
	}
	var dirtyErr *schema.DirtyTablesError
	if errors.As(err, &dirtyErr) {
		commit, migrate := "bd dolt commit", schema.SharedConsentCommand
		if globalFlag {
			commit, migrate = "bd dolt commit --global", schema.SharedConsentCommandGlobal
		}
		fmt.Fprintf(os.Stderr,
			"bd upgraded to %s: the pending schema migration(s) were NOT applied — they alter tables with uncommitted changes (%s).\n"+
				"Run '%s' to commit the working set at the current schema, then '%s'.\n"+
				"The next eligible non-preview command retries the migration.\n",
			displayVersion(Version), strings.Join(dirtyErr.Tables, ", "), commit, migrate)
		return
	}
	// Any other failure the reconciliation could not talk the operator out
	// of: name the underlying error and where to look, and keep the retry
	// promise — the witness was not consumed, so the next eligible command
	// tries again (and prints this again).
	fmt.Fprintf(os.Stderr,
		"bd upgraded to %s: the schema migration could not be applied — %v.\n"+
			"The next eligible non-preview command retries the migration; if it keeps failing, run 'bd doctor'.\n",
		displayVersion(Version), err)
}

// recoverPreV56IfNeeded rebuilds a Dolt database left behind by the pre-0.56
// embedded mode (GH#2137) — which means deleting .dolt — but only when the
// recorded predecessor is a release older than 0.56.0.
//
// Neither refusal below is redundant with the comparison:
//
//   - CompareVersions scans every dot-separated part with %d and leaves
//     whatever it cannot read at 0, so a stamp it cannot parse compares as
//     pre-0.56: Homebrew's "HEAD-<shortsha>" from a --HEAD install (#5603).
//     IsValidSemver rejects those shapes.
//   - A Go pseudo-version parses, but its numbers do not name a release.
//     "v0.55.5-0.<timestamp>-<hash>" is some commit after v0.55.4, and
//     "v0.0.0-<timestamp>-<hash>" a build with no tag in reach at all: the
//     prefix bounds the build from below, never from above, so it cannot show
//     that the workspace predates 0.56. IsValidSemver used to refuse the
//     v-prefixed ones some build paths stamp (#5650) only because it read
//     the "v" as a malformed part. Since it accepts a leading "v" (GH#6152),
//     a major-0 one can compare below 0.56.0, so pseudo-versions are refused
//     here explicitly, however they are spelled.
//
// A v-prefixed release ("v0.55.4") is compared numerically and still
// recovers. The .bd-dolt-ok marker RecoverPreV56DoltDir checks is the last
// gate, but only bd 0.58.0 and later write it: a database that only 0.56.x or
// 0.57.x has served has none, so for it these refusals are the only gate.
func recoverPreV56IfNeeded(previousVersion, dbPath string) {
	if !doctor.IsValidSemver(previousVersion) || isGoPseudoVersion(previousVersion) {
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

// isGoPseudoVersion reports whether a version stamp is a Go pseudo-version,
// with or without the leading "v". The module parser requires the "v", so the
// stamp is normalized the way the doctor version helpers read it (trimmed, one
// optional "v") and then given one.
func isGoPseudoVersion(version string) bool {
	return module.IsPseudoVersion("v" + strings.TrimPrefix(strings.TrimSpace(version), "v"))
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
