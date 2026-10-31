//go:build cgo

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

func usesSQLServer() bool {
	if shouldUseGlobals() {
		if serverMode || proxiedServerMode {
			return true
		}
	} else if cmdCtx != nil && (cmdCtx.ServerMode || cmdCtx.ProxiedServerMode) {
		return true
	}
	if doltserver.IsSharedServerMode() {
		return true
	}
	return false // default: embedded
}

// isEmbeddedMode reports whether the command is using embedded Dolt storage.
func isEmbeddedMode() bool {
	return !usesSQLServer()
}

// isEmbeddedStore reports whether st is an embedded Dolt store. Unlike
// isEmbeddedMode it answers for the store, not the command: a store opened in
// another workspace takes that workspace's backend (openDoltStoreFromConfig),
// so a server-mode command can hold an embedded one.
func isEmbeddedStore(st storage.DoltStorage) bool {
	_, ok := storage.UnwrapStore(st).(*embeddeddolt.EmbeddedDoltStore)
	return ok
}

func usesProxiedServer() bool {
	if shouldUseGlobals() {
		return proxiedServerMode
	}
	return cmdCtx != nil && cmdCtx.ProxiedServerMode
}

// newDoltStore creates a storage backend from an explicit config.
// Used by bd init and PersistentPreRun.
//
// Events-journal activation is applied HERE rather than by the caller: see the
// note at the top of events_journal.go for why every store construction has to
// go through an activating factory.
// newRegisteredBackendStore opens a store from the pluggable backend registry,
// so the registry arm of the root pre-run's open is not a second, unactivated
// construction path. A read-only open still goes through activation: a
// registered backend decides for itself what read-only means, and refusing to
// offer it the setting would be this factory guessing on its behalf.
func newRegisteredBackendStore(ctx context.Context, name, beadsDir string, readOnly bool) (s storage.DoltStorage, err error) {
	defer func() { s, err = activateVersionedHistoryStore(ctx, s, err) }()
	defer func() { s, err = activateEventsJournalStore(beadsDir, s, err) }()
	backend, ok := backends.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("storage backend %q is not registered", name)
	}
	if readOnly {
		return backend.OpenReadOnly(ctx, beadsDir)
	}
	return backend.Open(ctx, beadsDir)
}

func newDoltStore(ctx context.Context, cfg *dolt.Config) (s storage.DoltStorage, err error) {
	defer func() { s, err = activateVersionedHistoryStore(ctx, s, err) }()
	defer func() { s, err = activateEventsJournalStore(cfg.BeadsDir, s, err) }()
	if cfg.ProxiedServer {
		return nil, errProxiedStoreUnrouted()
	}
	if cfg.ServerMode {
		return dolt.New(ctx, cfg)
	}
	if cfg.Preview {
		// Preview commands are stricter than ordinary read commands: they
		// must not run schema initialization or permit incidental writes
		// before their RunE honors --dry-run/--inspect.
		return embeddeddolt.OpenForPreviewCommand(ctx, cfg.BeadsDir, cfg.Database, "main")
	}
	if cfg.ReadOnly {
		if cfg.DisableAutoStart {
			// Strict --readonly (cfg.DisableAutoStart is the strict-only
			// signal threaded from policy.disableAutoStart): the command
			// must not write anything, not even incidentally (schema
			// init, migrations, the post-command autocommit net). Use the
			// genuinely write-refusing open — same one used for cross-repo
			// hydration of foreign projects (GH#3231, bd-6dnrw.32) — instead
			// of OpenForReadOnlyCommand, which is "otherwise a normal
			// writable store".
			return embeddeddolt.OpenReadOnly(ctx, cfg.BeadsDir, cfg.Database, "main")
		}
		// Ordinary classified-read commands (bd show, bd list, ...) must
		// not be bricked by the #4259 remote-migrate gate (bd-578h9.5);
		// server mode's ReadOnly opens already skip migration entirely.
		return embeddeddolt.OpenForReadOnlyCommand(ctx, cfg.BeadsDir, cfg.Database, "main")
	}
	if cfg.LenientOpen {
		// Working-set-reconcile commands (bd dolt commit, bd vc commit) must
		// not be bricked by a pending-migration dirty-table refusal: that
		// refusal's documented recovery is exactly the commit these commands
		// run, so failing the open here would deadlock (#4566). The same goes
		// for the migration-consent refusal that precedes it. The server
		// arm above honors the same cfg.LenientOpen inside dolt.New; this
		// branch is the embedded half of one policy, not the whole of it.
		return embeddeddolt.OpenForWorkingSetReconcile(ctx, cfg.BeadsDir, cfg.Database, "main")
	}
	if cfg.RemoteSyncOpen {
		// `bd dolt pull` must not be bricked by the #6575 data-behind gate
		// refusal, because that refusal's entire remedy IS this pull: the gate
		// stops a clone that is behind the remote from migrating and tells the
		// operator to pull first, and an embedded clone has no external dolt
		// binary to do it with. Same deadlock as #4566, and this open tolerates
		// only that one gate reason (see openRemoteSync). The server arm above
		// honors the same cfg.RemoteSyncOpen inside dolt.New.
		return embeddeddolt.OpenForRemoteSync(ctx, cfg.BeadsDir, cfg.Database, "main")
	}
	return embeddeddolt.Open(ctx, cfg.BeadsDir, cfg.Database, "main")
}

// acquireEmbeddedLock acquires an exclusive flock on the embeddeddolt data
// directory derived from beadsDir. The caller must defer lock.Unlock().
// Returns a no-op lock when serverMode is true (the server handles its own
// concurrency).
func acquireEmbeddedLock(beadsDir string, serverMode bool) (util.Unlocker, error) {
	if serverMode {
		return util.NoopLock{}, nil
	}
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	lock, err := util.TryLock(filepath.Join(dataDir, ".lock"))
	if err != nil {
		if lockfile.IsLocked(err) {
			return nil, fmt.Errorf("embeddeddolt: another process holds the exclusive lock on %s; "+
				"the embedded backend supports only one writer at a time — "+
				"use the dolt server backend for concurrent access", dataDir)
		}
		return nil, fmt.Errorf("embeddeddolt: acquiring lock: %w", err)
	}
	return lock, nil
}

// newDoltStoreFromConfig creates a storage backend from the beads directory's
// persisted metadata.json configuration. Uses embedded Dolt by default;
// connects to dolt sql-server when dolt_mode is "server".
//
// For embedded mode, legacy hyphenated database names (pre-GH#2142) are
// auto-sanitized to underscores and the fix is persisted to metadata.json.
//
// This is the factory the CROSS-WORKSPACE opens use — routed creates,
// remote-cache hydration — so activation is resolved from beadsDir's own
// config, not the launching workspace's. effectiveServerMode below is what
// keeps that true for the dolt.shared-server layer; the one deliberate
// exception is BEADS_DOLT_SHARED_SERVER, which is machine-global in every
// resolver in the tree (see sharedServerModeForWorkspace).
func newDoltStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	return openDoltStoreFromConfig(ctx, beadsDir, false)
}

// newExistingDoltStoreFromConfig is newDoltStoreFromConfig for a write routed
// to another workspace (a prefix route or an auto-route, see
// openRoutedTargetStore): it opens that workspace's database writable, but
// refuses an embedded workspace that holds no database yet instead of creating
// one there. The routed open runs before the lookup it serves, so a created
// database would outlive an id that misses, and `bd bootstrap` would then take
// the empty database for the real one and skip the clone from sync.remote. The
// probe is embeddeddolt.HasRepository, the adapter's own marker check. A
// server-mode open already refuses a missing database: it does not set
// CreateIfMissing.
func newExistingDoltStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	return openDoltStoreFromConfig(ctx, beadsDir, true)
}

// openDoltStoreFromConfig is the body of newDoltStoreFromConfig and
// newExistingDoltStoreFromConfig; requireExisting selects the latter.
func openDoltStoreFromConfig(ctx context.Context, beadsDir string, requireExisting bool) (s storage.DoltStorage, err error) {
	defer func() { s, err = activateVersionedHistoryStore(ctx, s, err) }()
	defer func() { s, err = activateEventsJournalStore(beadsDir, s, err) }()
	// Before Load can migrate a legacy config.json; validateConfiguredBackend
	// below re-checks the loaded config.
	discovered, err := checkDiscoveredBackend(beadsDir)
	if err != nil {
		return nil, err
	}
	// A routed write that would find no embedded database is refused on the
	// discovered metadata, before Load migrates a legacy config.json: a
	// refused open writes nothing. Without metadata Load writes nothing
	// either, so that case, and unloadable metadata, are left to the check
	// after Load.
	if requireExisting && discovered != nil && selectsEmbeddedStore(beadsDir, discovered) && !embeddeddolt.HasRepository(beadsDir) {
		return nil, errNoRoutedEmbeddedDatabase(beadsDir)
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		// A present-but-unloadable metadata.json must not degrade to the
		// embedded default: on server-mode deployments the embedded
		// directory is an empty relic, and opening it silently turns every
		// query into an empty result set with exit 0 (false-empty). Absent
		// metadata.json (cfg == nil, err == nil) keeps the embedded default.
		return nil, fmt.Errorf("load %s: %w (refusing to fall back to the embedded store)", remedy.PrintedValue(configfile.ConfigPath(beadsDir)), remedy.PrintedError(err))
	}
	if err := validateConfiguredBackend(cfg, beadsDir); err != nil {
		return nil, err
	}
	cfg = normalizeLoadedConfig(cfg)
	if backend, ok := backends.Lookup(cfg.GetBackend()); ok {
		return backend.Open(ctx, beadsDir)
	}
	if cfg != nil && cfg.IsDoltProxiedServerMode() {
		return nil, errProxiedStoreUnrouted()
	}
	if effectiveServerMode(beadsDir, cfg) {
		return dolt.NewFromConfig(ctx, beadsDir)
	}
	// Refuse before the sanitize migration below, which renames the legacy
	// directory and rewrites metadata.json: a refused open writes nothing.
	if requireExisting && !embeddeddolt.HasRepository(beadsDir) {
		return nil, errNoRoutedEmbeddedDatabase(beadsDir)
	}
	database := configfile.DefaultDoltDatabase
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	if err := validateMetadataDatabaseName(database); err != nil {
		return nil, err
	}
	if sanitized := sanitizeDBName(database); sanitized != database {
		if err := migrateHyphenatedDB(beadsDir, cfg, database, sanitized); err != nil {
			return nil, fmt.Errorf("auto-sanitize database name %q → %q: %w", database, sanitized, err)
		}
		database = sanitized
	}
	return embeddeddolt.Open(ctx, beadsDir, database, "main")
}

// errNoRoutedEmbeddedDatabase refuses a routed write to beadsDir, an embedded
// workspace that holds no database (newExistingDoltStoreFromConfig).
func errNoRoutedEmbeddedDatabase(beadsDir string) error {
	return fmt.Errorf("embeddeddolt: no embedded database at %s; a routed write does not create one (run 'bd bootstrap' in that repository first)",
		remedy.PrintedValue(filepath.Join(beadsDir, "embeddeddolt")))
}

// migrateHyphenatedDB renames a legacy hyphenated database directory and
// persists the sanitized name to metadata.json so subsequent opens use it.
// This handles projects initialized before GH#2142 that upgrade to
// embedded-mode-default builds (GH#3231).
func migrateHyphenatedDB(beadsDir string, cfg *configfile.Config, oldName, newName string) error {
	for _, name := range []string{oldName, newName} {
		if err := validateMetadataDatabaseName(name); err != nil {
			return err
		}
	}
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	oldDir := filepath.Join(dataDir, oldName)
	newDir := filepath.Join(dataDir, newName)

	oldExists := false
	if info, err := os.Stat(oldDir); err == nil && info.IsDir() {
		oldExists = true
	}

	if oldExists {
		_, newErr := os.Stat(newDir)
		switch {
		case newErr == nil:
			return fmt.Errorf("cannot auto-migrate database: both %q and %q exist under %s; remove one manually and retry",
				oldName, newName, remedy.PrintedValue(dataDir))
		case !os.IsNotExist(newErr):
			return fmt.Errorf("checking target directory %q: %w", newDir, newErr)
		default:
			if err := os.Rename(oldDir, newDir); err != nil {
				return fmt.Errorf("renaming database directory: %w", err)
			}
			fmt.Fprintf(os.Stderr, "bd: migrated database directory %q → %q (GH#3231)\n", oldName, newName)
		}
	}

	if cfg != nil && cfg.DoltDatabase != newName {
		cfg.DoltDatabase = newName
		if err := cfg.Save(beadsDir); err != nil {
			return fmt.Errorf("persisting sanitized database name to metadata.json: %w", err)
		}
		fmt.Fprintf(os.Stderr, "bd: updated metadata.json dolt_database %q → %q (GH#3231)\n", oldName, newName)
	}
	return nil
}

func validateMetadataDatabaseName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return fmt.Errorf("invalid dolt_database %q", name)
	}
	return nil
}

// newReadOnlyStoreFromConfig creates a read-only storage backend from the beads
// directory's persisted metadata.json configuration.
//
// For embedded mode, invalid characters (hyphens, dots) are sanitized in-memory
// only — no directory renames or metadata.json writes. This prevents cross-repo
// hydration from mutating foreign projects (GH#3231).
func newReadOnlyStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	return openNonMutatingStoreFromConfig(ctx, beadsDir, false)
}

// newPreviewStoreFromConfig is newReadOnlyStoreFromConfig for a preview
// command (--dry-run, --inspect) reading a repository it was only pointed at:
// the same non-mutating open, but a BEHIND schema cursor is tolerated rather
// than refused, matching embeddeddolt.OpenForPreviewCommand and the preview
// policy the root pre-run applies to the command's own store. A preview that
// only reads a parent issue out of another repo should not be the thing that
// refuses because that repo has not been migrated yet — and it must certainly
// not migrate it.
func newPreviewStoreFromConfig(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
	return openNonMutatingStoreFromConfig(ctx, beadsDir, true)
}

// openNonMutatingStoreFromConfig deliberately does NOT activate the events
// journal: every arm below opens a store that refuses writes (OpenReadOnly,
// OpenForPreviewCommand, ReadOnly server config), so there is no mutation for a
// journal row to accompany. Registered in the construction guard's exemption
// list with that reason.
//
// The registry arm is the exception: it returns whatever the backend's
// OpenReadOnly means, and that one hook also serves the root pre-run's
// classified reads, `bd ready --claim` included, so it may be writable. The
// http client backend's read-only open is (httpclient.NewReadOnlyFromConfig),
// and a write through it reaches its bd serve. The journal exemption still
// holds there: recording that write is the business of the server that applies
// it, and the client has no journal of its own to activate. A separate
// OpenNonMutating hook on backends.Backend, which this function would call
// instead, is the follow-up that closes the exception.
func openNonMutatingStoreFromConfig(ctx context.Context, beadsDir string, preview bool) (storage.DoltStorage, error) {
	if _, err := checkDiscoveredBackend(beadsDir); err != nil {
		return nil, err
	}
	// A non-mutating open writes nothing into the workspace, its metadata
	// included: it reads a legacy config.json in place (LoadForDiscovery)
	// rather than migrating it into metadata.json, which drops the keys
	// Config does not know and deletes config.json. These factories serve
	// workspaces bd was not launched in (an external project, a routed or
	// auto-routed read), and such a workspace may be one bd run inside it
	// does not treat as a workspace at all: a clone of a SQLite-era project
	// keeps its tracked config.json and issues.jsonl, but not the ignored
	// *.db the historical SQLite check needs.
	cfg, err := configfile.LoadForDiscovery(beadsDir)
	if err != nil {
		// Same contract as newDoltStoreFromConfig: a present-but-unloadable
		// metadata.json is a hard error, not a silent embedded fallback —
		// and the error must name the real cause rather than the downstream
		// "database not found" the embedded open would produce.
		return nil, fmt.Errorf("load %s: %w (refusing to fall back to the embedded store)", remedy.PrintedValue(configfile.MetadataFilePath(beadsDir)), remedy.PrintedError(err))
	}
	if err := validateConfiguredBackend(cfg, beadsDir); err != nil {
		return nil, err
	}
	cfg = normalizeLoadedConfig(cfg)
	if backend, ok := backends.Lookup(cfg.GetBackend()); ok {
		return backend.OpenReadOnly(ctx, beadsDir)
	}
	if cfg != nil && cfg.IsDoltProxiedServerMode() {
		return nil, errProxiedStoreUnrouted()
	}
	if effectiveServerMode(beadsDir, cfg) {
		// A read refuses a schema too old for this binary's issue reads at
		// open; a preview tolerates a behind schema, as on the embedded arms.
		return dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true, RequireReadableSchema: !preview})
	}
	database := configfile.DefaultDoltDatabase
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	if err := validateMetadataDatabaseName(database); err != nil {
		return nil, err
	}
	if sanitized := sanitizeDBName(database); sanitized != database {
		database = sanitized
	}
	if preview {
		return embeddeddolt.OpenForPreviewCommand(ctx, beadsDir, database, "main")
	}
	// OpenReadOnly, not Open: a read-only open of a foreign project must not
	// run the remote-migrate gate (a behind, remote-backed database would fail
	// hard) and must not write migrations into the target's history
	// (bd-6dnrw.32, GH#3231).
	return embeddeddolt.OpenReadOnly(ctx, beadsDir, database, "main")
}
