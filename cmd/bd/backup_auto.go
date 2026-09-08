package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
)

// isBackupAutoEnabled returns whether backup should run.
// If user explicitly configured backup.enabled, use that.
// Otherwise, auto-enable when a git remote exists.
func isBackupAutoEnabled() bool {
	if config.GetValueSource("backup.enabled") != config.SourceDefault {
		return config.GetBool("backup.enabled")
	}
	return primeHasGitRemote()
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
// Detection bypasses Config.IsDoltServerMode() so it works correctly
// regardless of whether GH#3545's host-mode-inference fix is in
// place — the operator's intent is unambiguous from the host value
// alone.
func clientServerShareFilesystem() bool {
	host := os.Getenv("BEADS_DOLT_SERVER_HOST")
	if host == "" {
		// Fall back to in-struct config (config.yaml dolt.host etc.).
		host = config.GetString("dolt.host")
	}
	switch host {
	case "", "localhost", "127.0.0.1", "::1", "[::1]", "0.0.0.0":
		return true
	}
	return false
}

// autoBackupSkipNoticeOnce ensures the "auto-backup skipped" INFO
// message fires at most once per process — operators running long
// bd sessions don't need a chatty repeat on every command.
var autoBackupSkipNoticeOnce sync.Once

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
	if store == nil {
		return
	}
	if lm, ok := storage.UnwrapStore(store).(storage.LifecycleManager); ok && lm.IsClosed() {
		return
	}

	// GH#3523: when the Dolt server runs on a different filesystem
	// from this client (operator's BEADS_DOLT_SERVER_HOST points at a
	// non-localhost value), the file:// URL the auto-backup path
	// constructs is meaningless to the server — register fails on
	// every command. Skip cleanly with a one-time INFO so operators
	// know auto-backup is silent on purpose.
	if !clientServerShareFilesystem() {
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

	// Throttle: skip if we backed up recently
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
	currentCommit, err := store.GetCurrentCommit(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: auto-backup skipped: failed to get current commit: %v\n", err)
		return
	}
	if currentCommit == state.LastDoltCommit && state.LastDoltCommit != "" {
		debug.Logf("backup: no changes since last backup\n")
		return
	}

	// Run the backup (force=true since we already checked change detection above)
	if _, err := runBackupExport(ctx, true); err != nil {
		if !isQuiet() && !jsonOutput {
			fmt.Fprintf(os.Stderr, "Warning: auto-backup failed: %v\n", err)
		}
		debug.Logf("backup: error: %v\n", err)
		return
	}

	debug.Logf("backup: completed successfully\n")
}
