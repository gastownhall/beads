package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/syncbranch"
)

// showSyncStateStatus shows the current sync state per the spec.
// Output format:
//
//	Sync mode: git-portable
//	Last export: 2026-01-16 10:30:00 (commit abc123)
//	Pending changes: 3 issues modified since last export
//	Import branch: none
//	Conflicts: none
func showSyncStateStatus(ctx context.Context, jsonlPath string) error {
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	beadsDir := filepath.Dir(jsonlPath)

	// Sync mode (from config)
	syncCfg := config.GetSyncConfig()
	fmt.Printf("Sync mode: %s (%s)\n", syncCfg.Mode, SyncModeDescription(string(syncCfg.Mode)))
	fmt.Printf("  Export on: %s, Import on: %s\n", syncCfg.ExportOn, syncCfg.ImportOn)

	// Conflict strategy
	conflictCfg := config.GetConflictConfig()
	fmt.Printf("Conflict strategy: %s\n", conflictCfg.Strategy)

	// Federation config (if set)
	fedCfg := config.GetFederationConfig()
	if fedCfg.Remote != "" {
		fmt.Printf("Federation remote: %s\n", fedCfg.Remote)
		if fedCfg.Sovereignty != "" {
			fmt.Printf("  Sovereignty: %s\n", fedCfg.Sovereignty)
		}
	}

	// Last export time
	lastExport, err := store.GetMetadata(ctx, "last_import_time")
	if err != nil || lastExport == "" {
		fmt.Println("Last export: never")
	} else {
		// Try to parse and format nicely
		t, err := time.Parse(time.RFC3339Nano, lastExport)
		if err != nil {
			fmt.Printf("Last export: %s\n", lastExport)
		} else {
			// Try to get the last commit hash for the JSONL file
			commitHash := getLastJSONLCommitHash(ctx, jsonlPath)
			if commitHash != "" {
				fmt.Printf("Last export: %s (commit %s)\n", t.Format("2006-01-02 15:04:05"), commitHash[:7])
			} else {
				fmt.Printf("Last export: %s\n", t.Format("2006-01-02 15:04:05"))
			}
		}
	}

	// Dirty tracking removed (woho.4) — pending changes not tracked
	fmt.Println("Pending changes: not tracked (dirty tracking removed)")

	// Import branch (sync branch status)
	syncBranch, _ := syncbranch.Get(ctx, store)
	if syncBranch == "" {
		fmt.Println("Import branch: none")
	} else {
		fmt.Printf("Import branch: %s\n", syncBranch)
	}

	// Conflicts - check for sync conflict state file
	syncConflictPath := filepath.Join(beadsDir, "sync_conflicts.json")
	if _, err := os.Stat(syncConflictPath); err == nil {
		conflictState, err := LoadSyncConflictState(beadsDir)
		if err != nil {
			fmt.Println("Conflicts: unknown (error reading sync state)")
		} else if len(conflictState.Conflicts) > 0 {
			fmt.Printf("Conflicts: %d unresolved\n", len(conflictState.Conflicts))
			for _, c := range conflictState.Conflicts {
				fmt.Printf("  - %s: %s\n", c.IssueID, c.Reason)
			}
		} else {
			fmt.Println("Conflicts: none")
		}
	} else {
		fmt.Println("Conflicts: none")
	}

	return nil
}

// getLastJSONLCommitHash returns the short commit hash of the last commit
// that touched the JSONL file, or empty string if unknown.
func getLastJSONLCommitHash(ctx context.Context, jsonlPath string) string {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return ""
	}

	cmd := rc.GitCmd(ctx, "log", "-1", "--format=%h", "--", jsonlPath)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(output))
}

// staleSyncLockAge is the maximum age of a sync lock file before it's considered stale.
// Sync operations should complete well within this window, even for large repos.
const staleSyncLockAge = 1 * time.Hour

// cleanStaleSyncLock checks if the sync lock file is stale and removes it.
// Returns true if a stale lock was cleaned up.
// On Unix, flock is automatically released when a process exits, so this is a safety
// net for edge cases (NFS mounts, hung processes, container restarts).
func cleanStaleSyncLock(lockPath string) bool {
	info, err := os.Stat(lockPath)
	if err != nil {
		return false
	}

	age := time.Since(info.ModTime())
	if age <= staleSyncLockAge {
		return false
	}

	fmt.Fprintf(os.Stderr, "Warning: removing stale sync lock (age: %s)\n", age.Round(time.Second))
	if err := os.Remove(lockPath); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to remove stale sync lock: %v\n", err)
		return false
	}
	return true
}
