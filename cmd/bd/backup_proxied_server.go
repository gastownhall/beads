package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// Dolt-native backup over the proxied-server provider.
//
// The mechanism needed no proxy work at all: CALL DOLT_BACKUP(...) is ordinary
// SQL, RunNonTx already hands out a raw pinned connection (the seam `compact
// --dolt` and `gc` run on), the UOW DSN sets a connect timeout but no read
// deadline, and an active connection keeps the idle reaper from arming — so a
// backup that takes minutes is safe from both the driver and the 30s reaper.
//
// What the family DID need is the locality policy, which lives in the registry
// (see capability_registry.go's backup rows) because it is what the front door
// enforces. The guard below repeats it at the route because these functions are
// reachable from RunE, and a route that trusts the gate is one refactor away
// from writing a backup onto a host it does not own.
//
// None of these routes touch commandDidWrite or commandDidExplicitDoltCommit.
// Those two drive the post-run Dolt auto-commit, which is an embedded/direct
// concern — PersistentPostRunE's proxied branch never reads them — and the
// other proxied routes leave them alone for the same reason.

// proxiedBackupTargetName is the Dolt backup remote bd registers. Same name the
// direct path uses, so a workspace that changes topology still finds its
// destination.
const proxiedBackupTargetName = defaultDoltBackupName

// requireLocalProxiedBackup re-checks the locality policy at the route.
// Defense in depth, not a second policy: the rule and its wording come from the
// same registry row the pre-provider gate consults, so the two cannot drift.
func requireLocalProxiedBackup(path string) error {
	row, ok := LookupCapabilityRow(path, "")
	if !ok {
		// Unreachable: TestProxyCapabilityRegistryCoversCommandTree fails the
		// build for a backup path with no row. Refuse rather than guess.
		return HandleErrorRespectJSON("%s has no proxied-server capability row", path)
	}
	rule := row.ruleFor(resolveProxiedTopology(beads.FindBeadsDir()))
	if rule.Outcome == ProxyOutcomeHonored {
		return nil
	}
	return HandleProxyCapabilityError(proxyCapabilityErrorFor(rule))
}

// proxiedActiveDatabase asks the server which database the provider opened,
// rather than re-deriving it from the workspace config. The provider already
// resolved that question (metadata.json, then --database/--db), and a second
// ladder here would be a second answer waiting to disagree with it.
func proxiedActiveDatabase(ctx context.Context) (string, error) {
	var database string
	err := runProxiedNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var name sql.NullString
		if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&name); err != nil {
			return fmt.Errorf("resolve active database: %w", err)
		}
		if !name.Valid || name.String == "" {
			return fmt.Errorf("the proxied connection has no database selected")
		}
		database = name.String
		return nil
	})
	return database, err
}

// runBackupInitProxied registers the backup destination, mirroring the direct
// path's conflict handling: Dolt refuses a second remote at an address another
// one already holds, and a re-init to a new path has to replace the old entry
// rather than fail.
func runBackupInitProxied(ctx context.Context, rawPath string) error {
	if err := requireLocalProxiedBackup("backup init"); err != nil {
		return err
	}
	backupURL := resolveDoltBackupURL(rawPath)

	err := runProxiedNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		addErr := versioncontrolops.BackupAdd(ctx, conn, proxiedBackupTargetName, backupURL)
		if addErr == nil {
			return nil
		}
		switch {
		case strings.Contains(addErr.Error(), "already exists"):
			// Same name, different URL: replace it.
			_ = versioncontrolops.BackupRemove(ctx, conn, proxiedBackupTargetName)
		case versioncontrolops.ExtractAddressConflictName(addErr) != "":
			// A different name (auto-export registers "backup_export") already
			// points at this URL; take the address over.
			_ = versioncontrolops.BackupRemove(ctx, conn, versioncontrolops.ExtractAddressConflictName(addErr))
		default:
			return addErr
		}
		return versioncontrolops.BackupAdd(ctx, conn, proxiedBackupTargetName, backupURL)
	})
	if err != nil {
		return HandleErrorRespectJSON("failed to add backup destination: %v", err)
	}

	if err := saveDoltBackupConfig(backupURL); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: backup registered but failed to save config: %v\n", err)
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"backup_url":  backupURL,
			"backup_name": proxiedBackupTargetName,
			"initialized": true,
		})
	}
	fmt.Printf("Backup destination configured: %s\n", backupURL)
	fmt.Println("Run 'bd backup sync' to push your data.")
	return nil
}

// runBackupSyncProxied pushes the database to the configured destination.
func runBackupSyncProxied(ctx context.Context) error {
	if err := requireLocalProxiedBackup("backup sync"); err != nil {
		return err
	}

	start := time.Now()
	err := runProxiedNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		if err := commitWorkingSetBeforeBackup(ctx, conn); err != nil {
			// Same posture as the direct path: a failed pre-backup commit
			// means the backup may miss the newest writes, which is worth
			// saying out loud and is not worth refusing to back up over.
			fmt.Fprintf(os.Stderr, "Warning: failed to commit pending changes: %v\n", err)
		}
		return versioncontrolops.BackupSync(ctx, conn, proxiedBackupTargetName)
	})
	if err != nil {
		if strings.Contains(err.Error(), "no backup") || strings.Contains(err.Error(), "not found") {
			return HandleErrorRespectJSON("no backup destination configured. Run 'bd backup init <path>' first")
		}
		return HandleErrorRespectJSON("backup sync failed: %v", err)
	}

	elapsed := time.Since(start)
	if err := updateDoltBackupState(elapsed); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: backup synced but failed to update state: %v\n", err)
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"synced":   true,
			"duration": elapsed.String(),
		})
	}
	fmt.Printf("Backup synced in %s\n", elapsed.Round(time.Millisecond))
	return nil
}

// commitWorkingSetBeforeBackup is the proxied twin of the direct path's
// "bd: pre-backup commit". DOLT_BACKUP syncs COMMITTED history, so anything
// still in the working set would silently be missing from the backup.
//
// It is normally a no-op: every proxied write transaction ends in
// DOLT_COMMIT('-Am') (uow doltServerTx.Commit), so the working set is already
// clean. The pending check is what keeps this from minting an empty commit on
// every sync — Dolt rejects those server-side and logs each one.
func commitWorkingSetBeforeBackup(ctx context.Context, conn *sql.Conn) error {
	pending, err := issueops.HasPendingChanges(ctx, conn)
	if err != nil {
		return fmt.Errorf("check pending changes: %w", err)
	}
	if !pending {
		return nil
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)", "bd: pre-backup commit"); err != nil {
		if issueops.IsNothingToCommitError(err) {
			return nil
		}
		return err
	}
	return nil
}

// runBackupRemoveProxied unregisters the destination and drops the local
// sidecars. The backup data itself is left alone, as in direct mode.
func runBackupRemoveProxied(ctx context.Context) error {
	if err := requireLocalProxiedBackup("backup remove"); err != nil {
		return err
	}

	err := runProxiedNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		if err := versioncontrolops.BackupRemove(ctx, conn, proxiedBackupTargetName); err != nil {
			return err
		}
		// auto-export may have registered a second remote at the same URL.
		_ = versioncontrolops.BackupRemove(ctx, conn, "backup_export")
		return nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no backup") {
			return HandleErrorRespectJSON("no backup destination configured")
		}
		return HandleErrorRespectJSON("failed to remove backup: %v", err)
	}

	if path, err := doltBackupConfigPath(); err == nil {
		_ = os.Remove(path)
	}
	if path, err := doltBackupStatePath(); err == nil {
		_ = os.Remove(path)
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{"removed": true})
	}
	fmt.Println("Backup destination removed.")
	return nil
}

// doltBackupSizeProxied measures the managed-local database on disk, which is
// what `bd backup status` reports on every other topology that can answer.
//
// It reads the directory rather than asking Dolt because that is what the
// direct stores do (DoltStore.ActiveDatabaseSize measures
// localActiveDatabaseDir), so the number means the same thing in both modes.
// The database NAME still comes from the server, so the directory measured is
// the one this connection is actually using.
func doltBackupSizeProxied(ctx context.Context) (int64, bool, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return 0, false, nil
	}
	root, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return 0, false, nil
	}

	database, err := proxiedActiveDatabase(ctx)
	if err != nil {
		return 0, false, err
	}

	dir := filepath.Join(root, database)
	if _, err := os.Stat(dir); err != nil {
		// No local directory means nothing bd can measure; the status contract
		// treats size as optional rather than failing the command.
		return 0, false, nil
	}
	size, err := storage.MeasureDirectorySize(ctx, dir)
	if err != nil {
		return 0, false, fmt.Errorf("measure active database directory %q: %w", dir, err)
	}
	return size, true, nil
}

// runBackupRestoreProxied restores the database from a Dolt backup, quiescing
// the topology first.
//
// DOLT_BACKUP('restore') REPLACES the database under the server. Doing that
// while clients hold connections to it is undefined, so the restore runs with
// bd as the only client and nothing else pointed at the store:
//
//  1. `bd backup restore` already holds the workspace and physical-root gates
//     EXCLUSIVELY (commandNeedsExclusiveGate), so no other gated bd command —
//     including `bd serve` — is using this workspace.
//  2. This function closes the provider the root pre-run opened and shuts the
//     proxy and its dolt child down, so no connection survives into the
//     replace.
//  3. It reopens a provider of its own, which relaunches the topology with
//     exactly one client, and runs the restore on that.
//  4. It shuts the topology down again and reopens once more to finish: the
//     reopen proves the restored database is serviceable, and it is where the
//     project identity and the backup remote are reconciled. A connection
//     pinned across the replace would be reading a database that no longer
//     exists underneath it.
//
// The final shutdown leaves the workspace quiescent, so the next bd command
// relaunches lazily against the restored data.
func runBackupRestoreProxied(ctx context.Context, dir string, force bool) error {
	if err := requireLocalProxiedBackup("backup restore"); err != nil {
		return err
	}
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return HandleErrorRespectJSON("%s", activeWorkspaceNotFoundError())
	}
	root, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	backupURL, err := versioncontrolops.DirToFileURL(dir)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	// Ask the live provider which database to replace before taking it away.
	database, err := proxiedActiveDatabase(ctx)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if err := quiesceProxiedTopology(ctx, root); err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if err := withQuiescedProxiedProvider(ctx, beadsDir, database, root, func(ctx context.Context, conn *sql.Conn) error {
		return versioncontrolops.BackupRestore(ctx, conn, backupURL, database, force)
	}); err != nil {
		return HandleErrorRespectJSON("%s", proxiedRestoreFailureMessage(dir, false, err))
	}

	if err := withQuiescedProxiedProvider(ctx, beadsDir, database, root, func(ctx context.Context, conn *sql.Conn) error {
		return reconcileRestoredProxiedWorkspace(ctx, conn, beadsDir, dir, force)
	}); err != nil {
		return HandleErrorRespectJSON("%s", proxiedRestoreFailureMessage(dir, true, err))
	}

	return nil
}

// proxiedRestoreFailureMessage renders what the operator is told when one of
// the two quiesced steps fails.
//
// The distinction it makes is the only one that matters here: DID THE DATA COME
// BACK. A teardown failure happens after the step succeeded, so saying "restore
// failed" would be false, and the obvious response to it — run the restore
// again — is the wrong one. Split out from the call sites so that judgement is
// something a test can hold still (TestProxiedRestoreFailureMessage).
//
// afterReconcile distinguishes the two steps, because a teardown failure costs
// the operator different follow-up work either side of the reconcile: before
// it, the backup destination has not been re-registered yet.
func proxiedRestoreFailureMessage(dir string, afterReconcile bool, err error) string {
	var teardown *proxiedTeardownError
	if errors.As(err, &teardown) {
		if afterReconcile {
			return fmt.Sprintf("restored from %s, but shutting the proxied server down afterwards failed: %v; "+
				"the data is restored and reconciled — run 'bd dolt stop --force' before the next command", dir, teardown)
		}
		return fmt.Sprintf("restored from %s, but shutting the proxied server down afterwards failed: %v; "+
			"the data is restored — run 'bd dolt stop --force', then 'bd backup init %s' to re-register the "+
			"backup destination, which this run did not reach", dir, teardown, dir)
	}
	if afterReconcile {
		return fmt.Sprintf("restored from %s, but the restored database did not reopen: %v", dir, err)
	}
	return fmt.Sprintf("restore failed: %v", err)
}

// quiesceProxiedTopology drops this process's provider and stops the proxy and
// its dolt child, so nothing holds the database that is about to be replaced.
func quiesceProxiedTopology(ctx context.Context, root string) error {
	if uowProvider != nil {
		if err := uowProvider.Close(ctx); err != nil {
			return fmt.Errorf("close the proxied provider before restoring: %w", err)
		}
		uowProvider = nil
	}
	if err := proxy.Shutdown(root); err != nil {
		return fmt.Errorf("stop the proxied server before restoring: %w; "+
			"run 'bd dolt stop --force' and retry", err)
	}
	return nil
}

// proxiedTeardownError marks a failure that happened AFTER the operation
// succeeded, while putting the topology back down. It exists so the caller can
// tell the two apart in what it prints. Flattening them is what made a restore
// that completed and then failed to shut the proxy down report "restore
// failed": the operator is told their data did not come back when it did, and
// the obvious response — run the restore again — is the wrong one.
type proxiedTeardownError struct{ err error }

func (e *proxiedTeardownError) Error() string { return e.err.Error() }
func (e *proxiedTeardownError) Unwrap() error { return e.err }

// withQuiescedProxiedProvider relaunches the topology for one operation and
// puts it back down afterwards, so each step either side of the replace runs on
// a connection that was opened after it.
//
// The provider ADOPTS whatever identity the database carries: a restore is
// precisely the operation after which the workspace's recorded project id and
// the database's may legitimately differ, and asserting the old one would
// refuse the connection that is supposed to reconcile them. That makes this the
// third caller of newProxiedServerUOWProviderAdopting; its doc comment is the
// register of identity-assertion bypasses and names this one.
//
// A teardown failure is returned wrapped in *proxiedTeardownError, and only
// when the operation itself succeeded. If opening or running the operation
// failed, preserve that error and append any cleanup failure without the
// success marker. Shutdown is attempted even if construction fails after
// starting the topology but before returning a provider.
func withQuiescedProxiedProvider(ctx context.Context, beadsDir, database, root string, fn func(context.Context, *sql.Conn) error) (err error) {
	var provider uow.UnitOfWorkProvider
	defer func() {
		var closeErr error
		if provider != nil {
			closeErr = provider.Close(ctx)
		}
		stopErr := proxy.Shutdown(root)
		if teardownErr := errors.Join(closeErr, stopErr); teardownErr != nil {
			if err != nil {
				err = errors.Join(err, fmt.Errorf("shut down the proxied server after failure: %w", teardownErr))
			} else {
				err = &proxiedTeardownError{err: teardownErr}
			}
		}
	}()
	provider, err = newProxiedServerUOWProviderAdopting(ctx, beadsDir, database)
	if err != nil {
		return err
	}

	maintenance, ok := provider.(uow.MaintenanceProvider)
	if !ok {
		return fmt.Errorf("the proxied provider does not support maintenance operations")
	}
	return maintenance.RunNonTx(ctx, fn)
}

// reconcileRestoredProxiedWorkspace is the post-restore bookkeeping, mirroring
// the direct path: adopt the restored database's project identity when the
// restore overwrote one, and register the restore source as the backup
// destination so `bd backup sync` works without a separate init.
//
// Both are best-effort with a warning, as in direct mode. The restore itself
// has already succeeded by this point, and failing the command over bookkeeping
// would tell the operator their data did not come back when it did.
func reconcileRestoredProxiedWorkspace(ctx context.Context, conn *sql.Conn, beadsDir, dir string, force bool) error {
	if force {
		if err := syncProjectIDFromProxiedDB(ctx, conn, beadsDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to sync project ID after restore: %v\n", err)
		}
	}

	backupURL := resolveDoltBackupURL(dir)
	_ = versioncontrolops.BackupRemove(ctx, conn, proxiedBackupTargetName)
	if err := versioncontrolops.BackupAdd(ctx, conn, proxiedBackupTargetName, backupURL); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to register backup remote: %v\n", err)
		return nil
	}
	if err := saveDoltBackupConfig(backupURL); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: backup registered but failed to save config: %v\n", err)
	}
	return nil
}

// syncProjectIDFromProxiedDB copies the restored database's _project_id into
// metadata.json, so the identity check does not reject later connections. The
// direct path reads it through the store; there is no store in proxied mode, so
// it goes through the same issueops accessor the store itself uses rather than
// a second copy of the query.
func syncProjectIDFromProxiedDB(ctx context.Context, conn *sql.Conn, beadsDir string) error {
	dbID, err := issueops.GetMetadataInTx(ctx, conn, "_project_id")
	if err != nil || dbID == "" {
		return err
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return err
	}
	if cfg == nil || cfg.ProjectID == dbID {
		return nil
	}
	cfg.ProjectID = dbID
	return cfg.Save(beadsDir)
}
