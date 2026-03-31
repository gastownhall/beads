package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
)

// backupState tracks watermarks for backup change detection.
type backupState struct {
	LastDoltCommit string    `json:"last_dolt_commit"`
	Timestamp      time.Time `json:"timestamp"`
}

// backupDir returns the backup directory path, creating it if needed.
// When backup.git-repo is set to a valid git repo, returns a backup/ subdirectory
// inside that repo. Otherwise falls back to .beads/backup/.
func backupDir() (string, error) {
	gitRepo := config.GetString("backup.git-repo")
	if gitRepo != "" {
		if strings.HasPrefix(gitRepo, "~/") {
			home, _ := os.UserHomeDir()
			gitRepo = filepath.Join(home, gitRepo[2:])
		}
		if _, err := os.Stat(filepath.Join(gitRepo, ".git")); err != nil {
			debug.Logf("backup: git-repo %s is not a git repo, falling back to .beads/backup\n", gitRepo)
		} else {
			dir := filepath.Join(gitRepo, "backup")
			if err := os.MkdirAll(dir, 0700); err != nil {
				return "", fmt.Errorf("failed to create backup dir in git-repo: %w", err)
			}
			return dir, nil
		}
	}
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		beadsDir = ".beads"
	}
	dir := filepath.Join(beadsDir, "backup")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}
	return dir, nil
}

// loadBackupState reads the backup state file, returning a zero state if missing.
func loadBackupState(dir string) (*backupState, error) {
	path := filepath.Join(dir, "backup_state.json")
	data, err := os.ReadFile(path) //nolint:gosec // path is constructed internally
	if os.IsNotExist(err) {
		return &backupState{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read backup state: %w", err)
	}
	var state backupState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse backup state: %w", err)
	}
	return &state, nil
}

// saveBackupState writes the backup state file atomically.
func saveBackupState(dir string, state *backupState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal backup state: %w", err)
	}
	return atomicWriteFile(filepath.Join(dir, "backup_state.json"), data)
}

// atomicWriteFile writes data to a temp file and renames it into place (crash-safe).
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".backup-tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	return nil
}

// runBackupExport performs a Dolt-native backup to .beads/backup/.
// Returns the updated state.
func runBackupExport(ctx context.Context, force bool) (*backupState, error) {
	dir, err := backupDir()
	if err != nil {
		return nil, err
	}

	state, err := loadBackupState(dir)
	if err != nil {
		return nil, err
	}

	// Change detection: skip if nothing changed (unless forced)
	if !force {
		currentCommit, err := store.GetCurrentCommit(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get current commit: %w", err)
		}
		if currentCommit == state.LastDoltCommit && state.LastDoltCommit != "" {
			debug.Logf("backup: no changes since last backup (commit %s)\n", truncateHash(currentCommit))
			return state, nil
		}
	}

	bs, ok := storage.UnwrapStore(store).(storage.BackupStore)
	if !ok {
		return nil, fmt.Errorf("storage backend does not support backup operations")
	}

	if err := bs.BackupDatabase(ctx, dir); err != nil {
		return nil, err
	}

	// Update watermarks
	currentCommit, err := store.GetCurrentCommit(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current commit for state: %w", err)
	}
	state.LastDoltCommit = currentCommit
	state.Timestamp = time.Now().UTC()

	if err := saveBackupState(dir, state); err != nil {
		return nil, err
	}

	return state, nil
}

// truncateHash returns the first 8 characters of a hash, or the full string if shorter.
func truncateHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
