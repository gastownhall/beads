package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
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
		// restore from a silently skipped one.
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

// validateBackupRestoreDir checks dir looks like a real Dolt backup before
// any restore proceeds. Dolt's own CALL DOLT_BACKUP('restore', ...) drops
// the target database before it discovers the source holds no backup, so
// an existing-but-empty directory (or any directory without a Dolt
// manifest) previously passed this check and then destroyed the live
// database on the ensuing "not found" failure (GH#7098, GH#5972). The
// manifest file sits at the top level of a backup directory produced by
// `bd backup sync` / `dolt backup sync` -- confirmed against a real
// embedded backup, not assumed from documentation.
//
// This only covers local directory / file:// sources, which is the
// confirmed, reproduced case. Remote sources (gs://, aws://, http(s)://)
// are not validated here; see GH#7098's own note that the remote case was
// not reproduced and would need a different approach (restore into a
// scratch database, swap in only on success).
func validateBackupRestoreDir(dir string) error {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return fmt.Errorf("backup directory not found: %s\nRun 'bd backup' first to create a backup", dir)
	}
	if err != nil {
		return fmt.Errorf("backup directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("backup source is not a directory: %s", dir)
	}
	manifest := filepath.Join(dir, "manifest")
	if _, err := os.Stat(manifest); os.IsNotExist(err) {
		return fmt.Errorf("%s does not look like a Dolt backup (no manifest file found)\nRun 'bd backup' first to create a backup, or check the path is correct", dir)
	} else if err != nil {
		return fmt.Errorf("backup directory %s: %w", dir, err)
	}
	return nil
}
