package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
)

// isBackupAutoEnabled returns whether backup should run.
// If user explicitly configured backup.enabled, use that.
// Otherwise auto-enable when a git remote exists — BUT only in
// embedded mode.
//
// In sql-server / shared-server mode (usesSQLServer()) the default is
// OFF: N bd clients share a single Dolt server, and the Dolt-native
// backup path (store.BackupDatabase) registers a server-side backup
// remote under one fixed name pointing at THIS client's local
// .beads/backup dir, then full-syncs the whole DB. With many clients
// that means racing remove/add of the same name plus every client
// full-syncing the entire history into its own dir — the amplifier
// behind the 2026-07 shared-dolt CPU-pin incident. Operators who want
// backups in server mode must opt in explicitly (backup.enabled=true
// / BD_BACKUP_ENABLED=1) and coordinate destinations themselves.
func isBackupAutoEnabled() bool {
	if config.GetValueSource("backup.enabled") != config.SourceDefault {
		return config.GetBool("backup.enabled")
	}
	if usesSQLServer() {
		return false
	}
	return primeHasGitRemote()
}

// backupAutoStatusNote returns the parenthetical `bd backup status`
// prints after the effective backup.enabled value, or "" when the value
// speaks for itself. Callers pass the value isBackupAutoEnabled()
// already computed so the note cannot disagree with the number beside
// it — and so status does not re-run the git-remote probe.
//
// The note narrates the reason the decision actually used. An explicit
// backup.enabled needs no note: the source explains it, and every shape
// that reaches `bd backup status` honors it — including a managed-local
// proxied server, whose post-run arm runs auto-backup (every other proxied
// shape is refused by requireLocalProxiedBackup before status renders).
// A server-mode default is OFF whether or not a git remote exists, so it
// must not be attributed to the remote; before these arms existed, status
// did exactly that.
func backupAutoStatusNote(enabled bool) string {
	if config.GetValueSource("backup.enabled") != config.SourceDefault {
		return ""
	}
	if usesProxiedServer() {
		return "auto: off in proxied-server mode; set backup.enabled=true to opt in"
	}
	if usesSQLServer() {
		return "auto: off in sql-server mode"
	}
	if enabled {
		return "auto: git remote detected"
	}
	return "auto: no git remote"
}

// clientServerShareFilesystem reports whether the configured Dolt
// server runs on a filesystem the bd client can also see — i.e.
// whether a file:// URL constructed on the client is meaningful to
// the server.
//
// Returns true when the host is empty / localhost (embedded mode or
// local server), false when the host is set to a non-localhost
// value (external server in a container or remote machine).
//
// Used by maybeAutoBackup to skip the file:// auto-register that
// would otherwise fail every command (GH#3523). External-server
// operators who want auto-backup must configure an URL scheme that
// works cross-filesystem (s3://, gs://, etc.) — auto-backup's
// hardcoded file:// path can't help them.
//
// Detection follows the same effective-host precedence as
// configfile.GetDoltServerHost / HostImpliesServerMode (env >
// metadata.json > config.yaml, GH#3545), so a workspace whose remote
// host lives only in metadata.json is classified the same way here as
// by mode inference — the operator's intent is unambiguous from the
// effective host value alone.
func clientServerShareFilesystem() bool {
	host := os.Getenv("BEADS_DOLT_SERVER_HOST")
	if host == "" {
		if bd := beads.FindBeadsDir(); bd != "" {
			if cfg, err := configfile.Load(bd); err == nil && cfg != nil {
				// An explicit dolt_mode=embedded pins local storage;
				// a leftover dolt_server_host is inert then (same
				// gate as HostImpliesServerMode), so local
				// auto-backup stays available.
				if !strings.EqualFold(cfg.DoltMode, configfile.DoltModeEmbedded) {
					host = cfg.DoltServerHost
				}
			}
		}
	}
	if host == "" {
		// Fall back to in-struct config (config.yaml dolt.host etc.).
		host = config.GetString("dolt.host")
	}
	return configfile.IsLocalHostString(host)
}

// autoBackupSkipNoticeOnce ensures the "auto-backup skipped" INFO
// message fires at most once per process — operators running long
// bd sessions don't need a chatty repeat on every command.
var autoBackupSkipNoticeOnce sync.Once

// autoBackupBackendForCommand returns the storage this command's auto-backup
// runs against, or ok=false when there is nothing it may back up.
func autoBackupBackendForCommand() (localBackupBackend, bool) {
	if usesProxiedServer() {
		return proxiedAutoBackupBackend()
	}
	if store == nil {
		return nil, false
	}
	if lm, ok := storage.UnwrapStore(store).(storage.LifecycleManager); ok && lm.IsClosed() {
		return nil, false
	}
	return directLocalBackup{store: store}, true
}

// maybeAutoBackup runs a Dolt-native backup if enabled and the throttle interval has passed.
// Called from PersistentPostRun after auto-commit.
func maybeAutoBackup(ctx context.Context) {
	// Skip backup entirely when running as a git hook (post-checkout, post-merge, etc.).
	// Git hooks call 'bd hooks run' which goes through PersistentPostRun — without this
	// guard, every git checkout/merge/rebase triggers a backup on the current branch.
	if os.Getenv("BD_GIT_HOOK") == "1" {
		debug.Logf("backup: skipping — running as git hook\n")
		return
	}

	if !isBackupAutoEnabled() {
		return
	}
	backend, ok := autoBackupBackendForCommand()
	if !ok {
		return
	}

	// GH#3523: when the Dolt server runs on a different filesystem
	// from this client (operator's BEADS_DOLT_SERVER_HOST points at a
	// non-localhost value), the file:// URL the auto-backup path
	// constructs is meaningless to the server — register fails on
	// every command. Skip cleanly with a one-time INFO so operators
	// know auto-backup is silent on purpose.
	//
	// Proxied workspaces do not consult the host: proxiedAutoBackupBackend
	// has already required managed-local, where bd spawned the server on
	// this filesystem itself.
	if !usesProxiedServer() && !clientServerShareFilesystem() {
		autoBackupSkipNoticeOnce.Do(func() {
			if !isQuiet() && !jsonOutput {
				fmt.Fprintln(os.Stderr,
					"Info: auto-backup skipped — server filesystem differs "+
						"from client (BEADS_DOLT_SERVER_HOST is non-localhost).\n"+
						"      Configure backup.url=s3://... or run `bd backup` "+
						"manually for cross-filesystem backups.")
			}
		})
		debug.Logf("backup: skipping — server on remote filesystem\n")
		return
	}

	dir, err := backupDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: auto-backup skipped: %v\n", err)
		return
	}

	state, err := loadBackupState(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: auto-backup skipped: %v\n", err)
		return
	}

	// Throttle: skip if we backed up recently. Checked before the size-cap
	// walk below so the common case — the majority of bd invocations,
	// still inside the interval — pays zero directory-walk cost (ga-y6gjv
	// PR #6071 review: getDirSize's filepath.Walk cost 65-100ms per
	// invocation at 20k files when the cap check ran unconditionally,
	// before this throttle, on every single command).
	interval := config.GetDuration("backup.interval")
	if interval == 0 {
		interval = 15 * time.Minute
	}
	if !state.Timestamp.IsZero() && time.Since(state.Timestamp) < interval {
		debug.Logf("backup: throttled (last backup %s ago, interval %s)\n",
			time.Since(state.Timestamp).Round(time.Second), interval)
		return
	}

	// Change detection: skip if nothing changed
	currentCommit, err := backend.CurrentCommit(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: auto-backup skipped: failed to get current commit: %v\n", err)
		return
	}
	if currentCommit == state.LastDoltCommit && state.LastDoltCommit != "" {
		debug.Logf("backup: no changes since last backup\n")
		return
	}

	// Size cap: skip entirely once the destination has grown past
	// backup.size-cap-mb (ga-y6gjv) — see backupSizeCapExceeded for why a
	// cap, not an in-place prune, is the safe fix here.
	//
	// Placed AFTER change detection, which is the order
	// docs/reference/configuration.md:"How it works" documents. Ahead of
	// it, the walk ran on EVERY bd invocation indefinitely in an idle
	// workspace: nothing on the idle path advances state.Timestamp, so the
	// interval throttle above can never re-arm while nothing is changing
	// (ga-y6gjv PR #6071 review). Keep it above any lock acquisition —
	// a skip should not first take a lock it is about to release.
	if exceeded, size, err := backupSizeCapExceeded(dir); err != nil {
		warnBackupSizeCapUnavailable(err)
		// Re-arm the interval throttle before proceeding uncapped: not
		// every runBackupExport exit persists state.Timestamp (see
		// warnBackupSizeCapUnavailable), and without this the walk and
		// its warning would repeat on every bd command.
		state.Timestamp = time.Now().UTC()
		if saveErr := saveBackupState(dir, state); saveErr != nil {
			debug.Logf("backup: failed to persist throttle state after size cap error: %v\n", saveErr)
		}
	} else if exceeded {
		pauseAutoBackupForSizeCap(dir, state, size)
		return
	}

	// One backup at a time per workspace (backup_lock.go). Auto-backup never
	// waits: a held lock means a backup is already running.
	release, err := acquireBackupLock(0)
	if err != nil {
		if errors.Is(err, errBackupBusy) {
			debug.Logf("backup: skipping — another backup is running\n")
			return
		}
		fmt.Fprintf(os.Stderr, "Warning: auto-backup skipped: %v\n", err)
		return
	}
	defer release()

	// Run the backup (force=true since we already checked change detection above)
	if _, err := runBackupExport(ctx, backend, true); err != nil {
		if !isQuiet() && !jsonOutput {
			fmt.Fprintf(os.Stderr, "Warning: auto-backup failed: %v\n", err)
		}
		debug.Logf("backup: error: %v\n", err)
		return
	}

	debug.Logf("backup: completed successfully\n")
}
