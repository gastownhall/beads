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
	Use:   "restore [path]",
	Short: "Restore database from a Dolt backup",
	Long: `Restore the beads database from a Dolt-native backup.

By default, reads from .beads/backup/ (or the configured backup directory).
Optionally specify a path to a directory containing a Dolt backup.

This restores a full database backup created by 'bd backup sync' or an
equivalent Dolt backup. JSONL files produced by 'bd export' are issue exports,
not restore targets for this command.

Use --force to overwrite an existing database with the backup contents.

The database must already be initialized (run 'bd init' first if needed).
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
			dir, err = backupDir()
			if err != nil {
				return fmt.Errorf("failed to find backup directory: %w", err)
			}
		}

		if err := validateBackupRestoreDir(dir); err != nil {
			return err
		}

		force, _ := cmd.Flags().GetBool("force")

		if usesProxiedServer() {
			if err := runBackupRestoreProxied(ctx, dir, force); err != nil {
				return err
			}
		} else if err := runBackupRestore(ctx, store, dir, force); err != nil {
			return err
		}

		// One success report for both topologies. Under --json this used to
		// print nothing at all, which left a caller unable to tell a completed
		// restore from a silently skipped one. "source" is the argument
		// verbatim, not redacted like the prose echoes of it: it is data a
		// caller compares against what it passed, and RedactBackupURL has to
		// over-strip to fail closed (file:///srv/backups@2024/db would come back
		// as file://2024/db, a different location). It is the caller's own input
		// on its own stdout, as raw as the backup_url that 'bd backup init
		// --json' and 'bd backup status --json' already report.
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"restored": true,
				"source":   dir,
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

// validateBackupRestoreDir refuses a source that is plainly not there before
// either route runs, so the common typo gets the short answer instead of a
// Dolt error.
//
// A recognized backup URL is exempt, and that exemption is what makes remote
// restore reachable at all. This gate runs BEFORE the proxied/direct split, so
// statting the argument here rejected every URL-shaped source with "backup
// directory not found" on both topologies — os.Stat("s3://bucket/db") is
// IsNotExist — before any of the code that knows what a backup URL is could
// run. A recognized URL is checked by versioncontrolops.ResolveBackupSource,
// which every restore route goes through, this gate or not (bootstrap restores
// without it): it stats the directory a file:// URL names — the form bd itself
// persists and prints — and a remote URL has nothing local to stat. Its
// "backup source does not exist" error is load-bearing and test-asserted. An
// unrecognized scheme (S3://, az://) is not a backup URL, so it still lands
// here.
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
