package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// --- Dolt-native backup commands ---
//
// These wrap Dolt's built-in backup feature (CALL DOLT_BACKUP(...)) for standalone
// users who want their beads database backed up to a filesystem path, NAS, or DoltHub.
//
// Unlike the JSONL backup (bd backup), Dolt backups preserve full commit history
// and are faster for large databases.

const defaultDoltBackupName = "default"

var backupInitCmd = &cobra.Command{
	Use:     "init <path>",
	Aliases: []string{"add"},
	Short:   "Set up a Dolt backup destination",
	Long: `Configure a filesystem path or URL as a backup destination.

The path can be a local directory (external drive, NAS, Dropbox folder) or a
DoltHub remote URL. If the destination was previously configured, it is
updated to the new path.

Filesystem examples:
  bd backup add /mnt/usb/beads-backup
  bd backup add ~/Dropbox/beads-backup

DoltHub (recommended for cloud backup):
  bd backup add https://doltremoteapi.dolthub.com/myuser/beads-backup

After adding, run 'bd backup sync' to push your data.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := rootCtx
		rawPath := args[0]

		if store == nil {
			return fmt.Errorf("no store available")
		}

		// Resolve filesystem paths to absolute and add file:// prefix.
		// DoltHub URLs are passed through as-is.
		backupURL := resolveDoltBackupURL(rawPath)

		bs, ok := storage.UnwrapStore(store).(storage.BackupStore)
		if !ok {
			return fmt.Errorf("storage backend does not support backup operations")
		}

		// Register the backup with Dolt
		if err := bs.BackupAdd(ctx, defaultDoltBackupName, backupURL); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				// Same name, different URL — remove and re-add to update
				_ = bs.BackupRemove(ctx, defaultDoltBackupName)
				if err := bs.BackupAdd(ctx, defaultDoltBackupName, backupURL); err != nil {
					return fmt.Errorf("failed to update backup destination: %w", err)
				}
			} else if conflict := versioncontrolops.ExtractAddressConflictName(err); conflict != "" {
				// Different name (e.g. "backup_export") points at same URL — remove it, re-add as "default"
				_ = bs.BackupRemove(ctx, conflict)
				if err := bs.BackupAdd(ctx, defaultDoltBackupName, backupURL); err != nil {
					return fmt.Errorf("failed to add backup destination: %w", err)
				}
			} else {
				return fmt.Errorf("failed to add backup destination: %w", err)
			}
		}

		// Store the backup config in beads metadata for status display
		if err := saveDoltBackupConfig(backupURL); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: backup registered but failed to save config: %v\n", err)
		}

		commandDidWrite.Store(true)

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"backup_url":  backupURL,
				"backup_name": defaultDoltBackupName,
				"initialized": true,
			})
			return nil
		}

		fmt.Printf("Backup destination configured: %s\n", backupURL)
		fmt.Println("Run 'bd backup sync' to push your data.")
		return nil
	},
}

var backupSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Push database to configured Dolt backup",
	Long: `Sync the current beads database to the configured Dolt backup destination.

This pushes the entire database state (all branches, full history) to the
backup location configured with 'bd backup init'.

The backup is atomic — if the sync fails, the previous backup state is preserved.

Run 'bd backup init <path>' first to configure a destination.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := rootCtx
		if store == nil {
			return fmt.Errorf("no store available")
		}

		bs, ok := storage.UnwrapStore(store).(storage.BackupStore)
		if !ok {
			return fmt.Errorf("storage backend does not support backup operations")
		}

		// First, commit any pending changes so they're included in the backup
		committer, ok := storage.UnwrapStore(store).(storage.PendingCommitter)
		if !ok {
			return fmt.Errorf("storage backend does not support pending commits")
		}
		committed, err := committer.CommitPending(ctx, getActor())
		if err != nil && !strings.Contains(err.Error(), "nothing to commit") {
			fmt.Fprintf(os.Stderr, "Warning: failed to commit pending changes: %v\n", err)
		}
		if committed {
			commandDidExplicitDoltCommit = true
		}

		start := time.Now()

		// Sync to the configured backup
		if err := bs.BackupSync(ctx, defaultDoltBackupName); err != nil {
			if strings.Contains(err.Error(), "no backup") ||
				strings.Contains(err.Error(), "not found") {
				return fmt.Errorf("no backup destination configured. Run 'bd backup init <path>' first")
			}
			return fmt.Errorf("backup sync failed: %w", err)
		}

		elapsed := time.Since(start)

		// Update backup state
		if err := updateDoltBackupState(elapsed); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: backup synced but failed to update state: %v\n", err)
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"synced":   true,
				"duration": elapsed.String(),
			})
			return nil
		}

		fmt.Printf("Backup synced in %s\n", elapsed.Round(time.Millisecond))
		return nil
	},
}

// resolveDoltBackupURL converts a user-provided path or URL into a Dolt backup URL.
// Filesystem paths get resolved to absolute and prefixed with file://
// URLs (https://, http://) are passed through as-is.
func resolveDoltBackupURL(raw string) string {
	// DoltHub or other remote URLs — pass through
	if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") ||
		strings.HasPrefix(raw, "file://") || strings.HasPrefix(raw, "aws://") ||
		strings.HasPrefix(raw, "gs://") {
		return raw
	}

	// Expand ~ to home directory
	if strings.HasPrefix(raw, "~/") {
		home, _ := os.UserHomeDir()
		raw = filepath.Join(home, raw[2:])
	}

	// Resolve to absolute path
	abs, err := filepath.Abs(raw)
	if err != nil {
		abs = raw
	}

	return "file://" + abs
}

// doltBackupConfig stores the backup destination info in .beads/dolt-backup.json
type doltBackupConfig struct {
	BackupURL  string    `json:"backup_url"`
	BackupName string    `json:"backup_name"`
	CreatedAt  time.Time `json:"created_at"`
}

// doltBackupState tracks the last successful Dolt backup sync.
type doltBackupState struct {
	LastSync time.Time `json:"last_sync"`
	Duration string    `json:"duration"`
}

func doltBackupConfigPath() (string, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return "", fmt.Errorf("not in a beads repository")
	}
	return filepath.Join(beadsDir, "dolt-backup.json"), nil
}

func doltBackupStatePath() (string, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return "", fmt.Errorf("not in a beads repository")
	}
	return filepath.Join(beadsDir, "dolt-backup-state.json"), nil
}

func saveDoltBackupConfig(backupURL string) error {
	path, err := doltBackupConfigPath()
	if err != nil {
		return err
	}
	cfg := doltBackupConfig{
		BackupURL:  backupURL,
		BackupName: defaultDoltBackupName,
		CreatedAt:  time.Now().UTC(),
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data)
}

func loadDoltBackupConfig() (*doltBackupConfig, error) {
	path, err := doltBackupConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is constructed internally
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg doltBackupConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func updateDoltBackupState(duration time.Duration) error {
	path, err := doltBackupStatePath()
	if err != nil {
		return err
	}
	state := doltBackupState{
		LastSync: time.Now().UTC(),
		Duration: duration.String(),
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data)
}

func loadDoltBackupState() (*doltBackupState, error) {
	path, err := doltBackupStatePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is constructed internally
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state doltBackupState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// showDoltBackupStatus prints Dolt backup info as part of bd backup status.
func showDoltBackupStatus() {
	cfg, err := loadDoltBackupConfig()
	if err != nil || cfg == nil {
		return
	}

	fmt.Println("\nDolt Backup:")
	fmt.Printf("  Destination: %s\n", cfg.BackupURL)
	fmt.Printf("  Configured:  %s\n", cfg.CreatedAt.Format(time.RFC3339))

	state, err := loadDoltBackupState()
	if err != nil || state == nil {
		fmt.Println("  Last sync:   never")
		return
	}

	fmt.Printf("  Last sync:   %s (%s ago, took %s)\n",
		state.LastSync.Format(time.RFC3339),
		time.Since(state.LastSync).Round(time.Second),
		state.Duration)
}

// showDoltBackupStatusJSON returns Dolt backup info for JSON output.
func showDoltBackupStatusJSON() map[string]interface{} {
	result := map[string]interface{}{
		"configured": false,
	}

	cfg, err := loadDoltBackupConfig()
	if err != nil || cfg == nil {
		return result
	}

	result["configured"] = true
	result["backup_url"] = cfg.BackupURL
	result["backup_name"] = cfg.BackupName
	result["created_at"] = cfg.CreatedAt.Format(time.RFC3339)

	state, err := loadDoltBackupState()
	if err == nil && state != nil {
		result["last_sync"] = state.LastSync.Format(time.RFC3339)
		result["sync_duration"] = state.Duration
	}

	return result
}

// doltBackupSize returns the approximate size of the Dolt data directory in bytes.
func doltBackupSize() (int64, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return 0, fmt.Errorf("not in a beads repository")
	}
	dataDir := doltserver.ResolveDoltDir(beadsDir)
	return dirSize(dataDir)
}

// dirSize walks a directory tree and sums file sizes.
func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors (permission denied, etc.)
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

// showDBSize prints the database size as part of status.
func showDBSize() {
	size, err := doltBackupSize()
	if err != nil {
		return
	}
	fmt.Printf("  Database size: %s\n", formatBytes(size))
}

// showDBSizeJSON returns database size for JSON output.
func showDBSizeJSON() map[string]interface{} {
	size, err := doltBackupSize()
	if err != nil {
		return nil
	}
	return map[string]interface{}{
		"bytes": size,
		"human": formatBytes(size),
	}
}

var backupRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove the configured backup destination",
	Long: `Remove the configured backup destination.

This unregisters the backup remote from Dolt and removes the local
backup configuration. The backup data at the destination is not deleted.`,
	Aliases: []string{"rm"},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := rootCtx
		if store == nil {
			return fmt.Errorf("no store available")
		}

		bs, ok := storage.UnwrapStore(store).(storage.BackupStore)
		if !ok {
			return fmt.Errorf("storage backend does not support backup operations")
		}

		if err := bs.BackupRemove(ctx, defaultDoltBackupName); err != nil {
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no backup") {
				return fmt.Errorf("no backup destination configured")
			}
			return fmt.Errorf("failed to remove backup: %w", err)
		}

		// Also remove backup_export if it exists (auto-export may have created it at same URL)
		_ = bs.BackupRemove(ctx, "backup_export")

		// Remove local config
		if path, err := doltBackupConfigPath(); err == nil {
			_ = os.Remove(path)
		}
		if path, err := doltBackupStatePath(); err == nil {
			_ = os.Remove(path)
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{"removed": true})
			return nil
		}

		fmt.Println("Backup destination removed.")
		return nil
	},
}

func init() {
	backupCmd.AddCommand(backupInitCmd)
	backupCmd.AddCommand(backupSyncCmd)
	backupCmd.AddCommand(backupRemoveCmd)
}
