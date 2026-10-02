package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/ui"
)

var backupRestoreCmd = &cobra.Command{
	Use:   "restore [directory-or-url]",
	Short: "Restore database from a Dolt backup",
	Long: `Restore the beads database from a Dolt-native backup.

By default, reads from .beads/backup/ (or the configured backup directory).
Optionally specify a directory containing a Dolt backup or a remote backup URL.
A remote backup URL requires embedded or server mode; in proxied-server mode
this command restores from a local directory only.

This restores a full database backup created by 'bd backup sync' or an
equivalent Dolt backup. JSONL files produced by 'bd export' are issue exports,
not restore targets for this command.

Use --force to overwrite an existing database with the backup contents.

Restore requires a freshly initialized database. Run 'bd init' first if needed.
To initialize and restore in one step, use: bd init && bd backup restore`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("backup-restore")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx

		var dir string
		if len(args) > 0 {
			dir = args[0]
		} else {
			var err error
			if dir, err = defaultBackupRestoreDir(); err != nil {
				return err
			}
		}

		if err := validateBackupRestoreDir(dir); err != nil {
			return err
		}

		force, _ := cmd.Flags().GetBool("force")

		if usesProxiedServer() {
			// #5950's lower proxied route can resolve a URL, but executes the
			// restore through the Dolt server bd spawned as a child process. Its
			// remote support therefore comes from the installed Dolt binary, not
			// from the embedded engine this change upgrades. Keep this command's
			// proxied contract local-only until that server-side capability and
			// credential policy are settled; see backupRemoteSchemeTracking.
			if versioncontrolops.IsBackupURL(dir) {
				return HandleErrorRespectJSON(
					"backup restore from a URL is not supported in proxied-server mode: %s\n"+
						"The proxied route restores through the dolt server bd spawned, whose remote-backup "+
						"support comes from the installed dolt binary rather than the embedded engine.\n"+
						"Restore from a local directory, or run this workspace in embedded mode.",
					versioncontrolops.RedactBackupURL(dir))
			}
			if err := runBackupRestoreProxied(ctx, dir, force); err != nil {
				return err
			}
		} else if err := runBackupRestore(ctx, store, dir, force); err != nil {
			return err
		}

		// One success report for both topologies. Under --json this used to
		// print nothing at all, which left a caller unable to tell a completed
		// restore from a silently skipped one. Scripts and CI logs retain this
		// report, so a remote URL is redacted just like every diagnostic that
		// names it. Local directories and file:// URLs remain byte-identical.
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"restored": true,
				"source":   reportedRestoreSource(dir),
			})
		}
		fmt.Printf("%s Restore complete\n", ui.RenderPass("✓"))
		return nil
	},
}

func init() {
	backupRestoreCmd.Flags().Bool("force", false, "Overwrite existing database with backup contents")
	backupCmd.AddCommand(backupRestoreCmd)
}

// runBackupRestore restores the database from a Dolt-native backup.
func runBackupRestore(ctx context.Context, s storage.DoltStorage, dir string, force bool) error {
	if s == nil {
		return fmt.Errorf("database is not initialized. Run 'bd init' first")
	}

	bs, ok := storage.UnwrapStore(s).(storage.BackupStore)
	if !ok {
		return fmt.Errorf("storage backend does not support backup operations")
	}

	if err := bs.RestoreDatabase(ctx, dir, force); err != nil {
		return err
	}

	// After a force restore, the database's _project_id may differ from
	// metadata.json (the backup came from a different project). Sync
	// metadata.json to match the restored database so the identity check
	// doesn't reject subsequent connections.
	if force {
		if err := syncProjectIDFromDB(ctx, s); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to sync project ID after restore: %v\n", err)
		}
	}

	// Register the restore source as the backup destination so
	// `bd backup sync` works immediately without a separate `bd backup add`.
	registerBackupRemote(ctx, bs, dir)

	if err := s.Commit(ctx, "bd backup restore"); err != nil {
		if !strings.Contains(err.Error(), "nothing to commit") {
			return fmt.Errorf("failed to commit restore: %w", err)
		}
	}

	return nil
}

// registerBackupRemote registers dir as the default backup remote and saves
// the local backup config. Errors are non-fatal warnings.
func registerBackupRemote(ctx context.Context, bs storage.BackupStore, dir string) {
	backupURL := resolveDoltBackupURL(dir)

	// Remove + re-add to handle the case where a remote already exists.
	_ = bs.BackupRemove(ctx, defaultDoltBackupName)
	if err := bs.BackupAdd(ctx, defaultDoltBackupName, backupURL); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to register backup remote: %v\n", err)
		return
	}
	if err := saveDoltBackupConfig(backupURL); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: backup registered but failed to save config: %v\n", err)
	}
}

// syncProjectIDFromDB reads _project_id from the restored database and
// updates metadata.json to match, preventing identity mismatch errors.
func syncProjectIDFromDB(ctx context.Context, s storage.DoltStorage) error {
	dbID, err := s.GetMetadata(ctx, "_project_id")
	if err != nil || dbID == "" {
		return err
	}

	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return fmt.Errorf("%s; %s", activeWorkspaceNotFoundError(), diagHint())
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return err
	}

	if cfg.ProjectID == dbID {
		return nil // already in sync
	}

	cfg.ProjectID = dbID
	return cfg.Save(beadsDir)
}

// defaultBackupRestoreDir resolves the source of a no-argument restore: the
// backup directory, which auto-backup writes to whatever destination
// `bd backup init` recorded. A remote destination changes only what a
// directory with no recorded backup means. backupDir() MkdirAll's its answer,
// so with nothing backed up locally the restore would pass the existence check
// on the directory it just created and fail deep inside Dolt over a file://
// source the operator never chose — that case is refused, naming the
// destination. A directory holding a backup is restored as it always was, with
// a note that the destination was not used.
func defaultBackupRestoreDir() (string, error) {
	dir, err := backupDir()
	if err != nil {
		return "", fmt.Errorf("failed to find backup directory: %w", err)
	}
	remote := persistedRemoteBackupURL()
	if remote == "" {
		return dir, nil
	}
	// Every completed auto-backup records its commit here, the same test
	// `bd backup status` uses for "a backup has been performed". A state file
	// that cannot be read keeps the old behavior: the refusal exists to
	// replace an opaque failure, not to block a restore that may work.
	if state, err := loadBackupState(dir); err != nil || state.LastDoltCommit != "" {
		if !isQuiet() && !jsonOutput {
			fmt.Fprintf(os.Stderr, "Note: restoring the local backup in %s; the configured backup destination (%s) is used only when passed explicitly.\n", dir, remote)
		}
		return dir, nil
	}
	hint := "Pass a directory that holds a Dolt backup."
	if !usesProxiedServer() {
		hint = "Pass the backup URL explicitly, or pass a directory that holds a Dolt backup."
	}
	return "", HandleErrorRespectJSON(
		"the configured backup destination is a remote URL (%s), which 'bd backup restore' does not use by default,\n"+
			"and no local backup is recorded in %s.\n"+
			"%s",
		remote, dir, hint)
}

// persistedRemoteBackupURL returns the backup destination recorded in
// .beads/dolt-backup.json, redacted, when it is a remote backup URL — one
// backupDir() has no way to name. A missing, unreadable or malformed config
// returns "", as does a file:// destination (see isRemoteBackupURL).
func persistedRemoteBackupURL() string {
	cfg, err := loadDoltBackupConfig()
	if err != nil || cfg == nil || !isRemoteBackupURL(cfg.BackupURL) {
		return ""
	}
	return versioncontrolops.RedactBackupURL(cfg.BackupURL)
}

// isRemoteBackupURL reports whether s is a backup URL naming somewhere other
// than the local filesystem. file:// is excluded: resolveDoltBackupURL writes
// file://<abs> for every local `bd backup init`, so counting those as remote
// would refuse the no-argument restore for the population that has always
// used it, and a local path carries no credential to redact.
func isRemoteBackupURL(s string) bool {
	return versioncontrolops.IsBackupURL(s) && !strings.HasPrefix(s, "file://")
}

// reportedRestoreSource is the source as the --json success report echoes it.
// Only remote URLs can carry credentials and are passed through #5949's
// canonical redactor. A directory or file:// path is echoed exactly as passed.
func reportedRestoreSource(source string) string {
	if isRemoteBackupURL(source) {
		return versioncontrolops.RedactBackupURL(source)
	}
	return source
}

// validateBackupRestoreDir refuses a source that is plainly not there before
// either route runs, so the common typo gets the short answer instead of a
// Dolt error.
//
// A recognized backup URL is exempt, and that exemption makes remote restore
// reachable on direct routes. This gate runs BEFORE the proxied/direct split;
// the E layer applies its stricter proxied URL policy immediately afterward.
// Without the exemption, os.Stat("s3://bucket/db") rejects the source before
// any URL-aware code can run. ResolveBackupSource remains the storage-level
// authority: it stats the directory a file:// URL names and passes a remote URL
// through because there is nothing local to stat. An unrecognized scheme
// (S3://, az://) is not a backup URL, so it still lands here.
func validateBackupRestoreDir(dir string) error {
	if versioncontrolops.IsBackupURL(dir) {
		return nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		// An unrecognized scheme reaches this line, so the source can still
		// carry credentials.
		return fmt.Errorf("backup directory not found: %s\nRun 'bd backup' first to create a backup",
			versioncontrolops.RedactBackupURL(dir))
	}
	return nil
}
