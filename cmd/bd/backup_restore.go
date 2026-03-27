package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/ui"
)

var backupRestoreCmd = &cobra.Command{
	Use:   "restore [path]",
	Short: "Restore database from a Dolt backup",
	Long: `Restore the beads database from a Dolt-native backup.

By default, reads from .beads/backup/ (or the configured backup directory).
Optionally specify a path to a directory containing a Dolt backup.

Use --force to overwrite an existing database with the backup contents.

The database must already be initialized (run 'bd init' first if needed).
To initialize and restore in one step, use: bd init && bd backup restore`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
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

		if err := runBackupRestore(ctx, store, dir, force); err != nil {
			return err
		}

		if !jsonOutput {
			fmt.Printf("%s Restore complete\n", ui.RenderPass("✓"))
		}

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

	bs, ok := s.(storage.BackupStore)
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

	if err := s.Commit(ctx, "bd backup restore"); err != nil {
		if !strings.Contains(err.Error(), "nothing to commit") {
			return fmt.Errorf("failed to commit restore: %w", err)
		}
	}

	return nil
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
		return fmt.Errorf("cannot find .beads directory")
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

func validateBackupRestoreDir(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("backup directory not found: %s\nRun 'bd backup' first to create a backup", dir)
	}
	return nil
}
