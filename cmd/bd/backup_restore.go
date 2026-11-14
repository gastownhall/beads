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

		proxied := usesProxiedServer()
		// A store that cannot restore at all (a bd connect workspace) refuses
		// before the source is checked, so its user is not sent off to write a
		// backup that this workspace could never restore. A proxied restore
		// runs only on the managed-local server bd spawned on this machine
		// (runBackupRestoreProxied refuses every other topology).
		reader := restoreSourceReadByBd
		if !proxied {
			if _, err := restoreBackupStore(store); err != nil {
				return err
			}
			reader = restoreSourceReaderOf(store)
		}
		if err := validateBackupRestoreDir(dir, reader); err != nil {
			return err
		}

		force, _ := cmd.Flags().GetBool("force")

		if proxied {
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

// restoreBackupStore returns the restore surface of s, or the refusal for a
// store that has none.
func restoreBackupStore(s storage.DoltStorage) (storage.BackupStore, error) {
	if s == nil {
		return nil, fmt.Errorf("database is not initialized. Run 'bd init' first")
	}
	bs, ok := storage.UnwrapStore(s).(storage.BackupStore)
	if !ok {
		return nil, fmt.Errorf("storage backend does not support backup operations")
	}
	return bs, nil
}

// runBackupRestore restores the database from a Dolt-native backup.
func runBackupRestore(ctx context.Context, s storage.DoltStorage, dir string, force bool) error {
	bs, err := restoreBackupStore(s)
	if err != nil {
		return err
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

// restoreSourceReader is what opens a restore source's directory, which
// decides what bd's own check of that directory says about it.
type restoreSourceReader int

const (
	// restoreSourceReadByBd is embedded Dolt in this process, or the
	// managed-local server a proxied workspace runs on this machine: the
	// check reads the directory the restore opens.
	restoreSourceReadByBd restoreSourceReader = iota
	// restoreSourceReadByLocalServer is a Dolt SQL server at a loopback host
	// (clientServerShareFilesystem), taken to share this machine's
	// filesystem. It opens the path on its own filesystem all the same, so
	// in a container it opens whatever is at that path inside it.
	restoreSourceReadByLocalServer
	// restoreSourceReadByRemoteServer is a Dolt SQL server at another host,
	// which opens the path on a filesystem bd cannot check.
	restoreSourceReadByRemoteServer
)

// restoreSourceReaderOf returns what opens the source of a restore into s, a
// store that is not proxied: the engine in this process for embedded Dolt,
// otherwise the Dolt SQL server s is connected to.
func restoreSourceReaderOf(s storage.DoltStorage) restoreSourceReader {
	switch {
	case isEmbeddedStore(s):
		return restoreSourceReadByBd
	case clientServerShareFilesystem():
		return restoreSourceReadByLocalServer
	default:
		return restoreSourceReadByRemoteServer
	}
}

// backupRestoreRemedy is the hint every restore preflight refusal of the
// source ends with.
const backupRestoreRemedy = "Restore was not attempted. Pass a directory written by 'bd backup sync';\n" +
	"to create one, run 'bd backup init <path>' and then 'bd backup sync'"

// backupRestoreServerNote follows backupRestoreRemedy when a Dolt SQL server
// at a loopback host opens the source. A server in a container there opens
// the path inside the container, where its `bd backup sync` wrote too, so a
// backup it holds is refused here unless this machine sees the same backup at
// the same path.
const backupRestoreServerNote = "bd checked the path on this machine, and the Dolt server opens it on its own filesystem:\n" +
	"a server in a container needs the backup mounted at the same path in both"

// validateBackupRestoreDir refuses a restore source that holds no Dolt backup,
// or sits at a path Dolt would read as another directory, before either
// restore path starts. A forced restore opens the source, replaces the live
// database, and only then syncs the backup into it, so an empty or wrong
// directory (including the default .beads/backup, which backupDir creates)
// would otherwise cost the database (GH#7098). The check itself is the
// storage layer's (versioncontrolops.RestoreSourceURL), the one each restore
// path builds its source URL with; running it here first refuses before the
// proxied path quiesces the topology.
//
// The check reads this machine's filesystem, and in server mode the Dolt
// server opens the path on its own (reader). A server at another host reads
// a filesystem bd cannot check, so a restore from it is refused outright: a
// forced one from a directory the server sees as empty would drop the
// database, and one without --force cannot replace the workspace's existing
// database anyway. A server at a loopback host is taken to share this
// filesystem, and its refusals say which side was checked; one in a
// container that is handed a backup only this machine holds still drops the
// database under --force, until the engine restores through a staging
// database (dolthub/dolt#11951).
func validateBackupRestoreDir(dir string, reader restoreSourceReader) error {
	if reader == restoreSourceReadByRemoteServer {
		return fmt.Errorf("backup source %s is read by the Dolt server at %s from its own filesystem, which bd cannot check from this machine;\n"+
			"a --force restore from a directory that holds no Dolt backup there drops the database\n"+
			"Restore was not attempted. Run 'bd backup restore' on the server's machine, in a workspace\n"+
			"that reaches the server at localhost, so bd checks the directory the server reads",
			remedy.PrintedValue(dir), remedy.PrintedValue(backupServerHost()))
	}
	hint := backupRestoreRemedy
	if reader == restoreSourceReadByLocalServer {
		hint += "\n" + backupRestoreServerNote
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("backup directory not found: %s\n%s", dir, hint)
	}
	if _, err := versioncontrolops.RestoreSourceURL(dir); err != nil {
		return fmt.Errorf("%w\n%s", err, hint)
	}
	return nil
}
