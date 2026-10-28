package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/remedy"
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
		CheckReadonly("backup restore")

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

		if err := validateBackupRestoreDir(dir, restoreReadsLocalSource()); err != nil {
			return err
		}

		force, _ := cmd.Flags().GetBool("force")

		if usesProxiedServer() {
			if err := runBackupRestoreProxied(ctx, dir, force); err != nil {
				return err
			}
		} else if err := runBackupRestore(ctx, store, dir, force); err != nil {
			if errors.Is(err, versioncontrolops.ErrNotDoltBackup) {
				return notDoltBackupRefusal(dir, err)
			}
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

// validateBackupRestoreDir refuses a restore source before anything touches
// the database: a missing directory, a path Dolt would read as a different
// directory, and, when localSource is set, a directory that holds no Dolt
// backup, which a forced restore would otherwise drop the target for before
// failing (#5972).
//
// localSource says this process's filesystem is the one the restore reads
// (restoreReadsLocalSource). Without it the backup can be on a Dolt server bd
// cannot see into, so whether the directory holds one is left to
// versioncontrolops.BackupRestore, which asks the server before the restore.
// The directory itself must still exist here in every mode, since the store
// stats it too: a backup at a path only the server has is refused as missing
// before the server is asked.
func validateBackupRestoreDir(dir string, localSource bool) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("backup directory not found: %s\nRun 'bd backup' first to create a backup", dir)
	}
	check := versioncontrolops.CheckBackupPath
	if localSource {
		check = versioncontrolops.CheckBackupDir
	}
	if err := check(dir); err != nil {
		if errors.Is(err, versioncontrolops.ErrNotDoltBackup) {
			return notDoltBackupRefusal(dir, err)
		}
		return err
	}
	return nil
}

// restoreReadsLocalSource reports whether the restore reads its source
// directory on this process's filesystem: embedded Dolt runs in this process,
// and proxied-server mode honors a restore only on managed-local, where bd
// spawned the server itself. A direct server-mode connection can reach a Dolt
// server on another host or in a container, which reads the path on its own
// filesystem.
func restoreReadsLocalSource() bool {
	return isEmbeddedMode() || usesProxiedServer()
}

// notDoltBackupRefusal words err, a refusal of dir as a source that holds no
// Dolt backup (versioncontrolops.ErrNotDoltBackup), for the user: the
// database was not changed, and what to run instead. Backing up into dir is
// offered only when nothing better is known: `bd backup init` replaces the
// configured backup destination, so after a mistyped path, or on a directory
// of JSONL exports, it would point the next sync at the wrong directory.
func notDoltBackupRefusal(dir string, err error) error {
	var next []string
	entries, _ := os.ReadDir(dir)
	if slices.ContainsFunc(entries, func(e os.DirEntry) bool { return !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") }) {
		next = append(next, "It holds JSONL exports, which are imported, not restored: run bd import <file>")
	}
	if cfg, _ := loadDoltBackupConfig(); cfg != nil && cfg.BackupURL != "" {
		next = append(next, "The configured backup destination is "+remedy.PrintedValue(cfg.BackupURL)+" (bd backup status)")
	}
	if len(next) == 0 {
		next = append(next, "To back up into it, run "+backupInitCommand(dir)+", then bd backup sync")
	}
	return fmt.Errorf("%w; the database was not changed\n%s", err, strings.Join(next, "\n"))
}
