package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/merge"
	"github.com/steveyegge/beads/internal/storage"
)

// isIssueNotFoundError checks if the error indicates the issue doesn't exist in the database.
//
// During 3-way merge, we try to delete issues that were removed remotely. However, the issue
// may already be gone from the local database due to:
//   - Already tombstoned by a previous sync/import
//   - Never existed locally (multi-repo scenarios, partial clones)
//   - Deleted by user between export and import phases
//
// In all these cases, "issue not found" is success from the merge's perspective - the goal
// is to ensure the issue is deleted, and it already is. We only fail on actual database errors.
func isIssueNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "issue not found:")
}

// getVersion returns the current bd version
func getVersion() string {
	return Version
}

// captureLeftSnapshot copies the current JSONL to the left snapshot file
// This should be called after export, before git pull
func captureLeftSnapshot(jsonlPath string) error {
	sm := NewSnapshotManager(jsonlPath)
	return sm.CaptureLeft()
}

// updateBaseSnapshot copies the current JSONL to the base snapshot file
// This should be called after successful import to track the new baseline
func updateBaseSnapshot(jsonlPath string) error {
	sm := NewSnapshotManager(jsonlPath)
	return sm.UpdateBase()
}

// merge3WayAndPruneDeletions performs 3-way merge and prunes accepted deletions from DB
// Returns true if merge was performed, false if skipped (no base file)
func merge3WayAndPruneDeletions(ctx context.Context, store storage.Storage, jsonlPath string) (bool, error) {
	sm := NewSnapshotManager(jsonlPath)
	basePath, leftPath := sm.getSnapshotPaths()

	// If no base snapshot exists, skip deletion handling (first run or bootstrap)
	if !sm.BaseExists() {
		return false, nil
	}

	// Validate snapshot metadata
	if err := sm.Validate(); err != nil {
		// Stale or invalid snapshot - clean up and skip merge
		fmt.Fprintf(os.Stderr, "Warning: snapshot validation failed (%v), cleaning up\n", err)
		_ = sm.Cleanup()
		return false, nil
	}

	// Run 3-way merge: base (last import) vs left (pre-pull export) vs right (pulled JSONL)
	tmpMerged := jsonlPath + ".merged"
	// Ensure temp file cleanup on failure
	defer func() {
		if fileExists(tmpMerged) {
			_ = os.Remove(tmpMerged)
		}
	}()

	if err := merge.Merge3Way(tmpMerged, basePath, leftPath, jsonlPath, false); err != nil {
		// Merge error (including conflicts) is returned as error
		return false, fmt.Errorf("3-way merge failed: %w", err)
	}

	// Replace the JSONL with merged result
	if err := os.Rename(tmpMerged, jsonlPath); err != nil {
		return false, fmt.Errorf("failed to replace JSONL with merged result: %w", err)
	}

	// Compute accepted deletions (issues in base but not in merged, and unchanged locally)
	acceptedDeletions, err := sm.ComputeAcceptedDeletions(jsonlPath)
	if err != nil {
		return false, fmt.Errorf("failed to compute accepted deletions: %w", err)
	}

	// Prune accepted deletions from the database.
	//
	// "Accepted deletions" are issues that:
	//   1. Existed in the base snapshot (last successful import)
	//   2. Were NOT modified locally (still in left snapshot, unchanged)
	//   3. Are NOT in the merged result (deleted remotely)
	//
	// We tolerate "issue not found" errors because the issue may already be gone:
	//   - Tombstoned by auto-import's git-history-backfill
	//   - Deleted manually by the user
	//   - Never existed in this clone (multi-repo, partial history)
	// The goal is ensuring deletion, so already-deleted is success.
	var deletionErrors []error
	var alreadyGone int
	for _, id := range acceptedDeletions {
		if err := store.DeleteIssue(ctx, id); err != nil {
			if isIssueNotFoundError(err) {
				alreadyGone++
				continue
			}
			deletionErrors = append(deletionErrors, fmt.Errorf("issue %s: %w", id, err))
		}
	}

	if len(deletionErrors) > 0 {
		return false, fmt.Errorf("deletion failures (DB may be inconsistent): %v", deletionErrors)
	}

	// Print stats if deletions were found
	stats := sm.GetStats()
	actuallyDeleted := len(acceptedDeletions) - alreadyGone
	if stats.DeletionsFound > 0 || alreadyGone > 0 {
		if alreadyGone > 0 {
			fmt.Fprintf(os.Stderr, "3-way merge: pruned %d deleted issue(s) from database, %d already gone (base: %d, left: %d, merged: %d)\n",
				actuallyDeleted, alreadyGone, stats.BaseCount, stats.LeftCount, stats.MergedCount)
		} else {
			fmt.Fprintf(os.Stderr, "3-way merge: pruned %d deleted issue(s) from database (base: %d, left: %d, merged: %d)\n",
				actuallyDeleted, stats.BaseCount, stats.LeftCount, stats.MergedCount)
		}
	}

	return true, nil
}

// getSnapshotStats returns statistics about the snapshot files
// Deprecated: Use SnapshotManager.GetStats() instead
func getSnapshotStats(jsonlPath string) (baseCount, leftCount int, baseExists, leftExists bool) {
	sm := NewSnapshotManager(jsonlPath)
	basePath, leftPath := sm.GetSnapshotPaths()

	if baseIDs, err := sm.BuildIDSet(basePath); err == nil && len(baseIDs) > 0 {
		baseExists = true
		baseCount = len(baseIDs)
	} else {
		baseExists = fileExists(basePath)
	}

	if leftIDs, err := sm.BuildIDSet(leftPath); err == nil && len(leftIDs) > 0 {
		leftExists = true
		leftCount = len(leftIDs)
	} else {
		leftExists = fileExists(leftPath)
	}

	return
}

// initializeSnapshotsIfNeeded creates initial snapshot files if they don't exist
// Deprecated: Use SnapshotManager.Initialize() instead
func initializeSnapshotsIfNeeded(jsonlPath string) error {
	sm := NewSnapshotManager(jsonlPath)
	return sm.Initialize()
}

// getMultiRepoJSONLPaths returns all JSONL file paths for multi-repo mode
// Returns nil if not in multi-repo mode
func getMultiRepoJSONLPaths() []string {
	multiRepo := config.GetMultiRepoConfig()
	if multiRepo == nil {
		return nil
	}

	var paths []string

	// Primary repo JSONL
	primaryPath := multiRepo.Primary
	if primaryPath == "" {
		primaryPath = "."
	}
	primaryJSONL := filepath.Join(primaryPath, ".beads", "issues.jsonl")
	paths = append(paths, primaryJSONL)

	// Additional repos' JSONLs
	for _, repoPath := range multiRepo.Additional {
		jsonlPath := filepath.Join(repoPath, ".beads", "issues.jsonl")
		paths = append(paths, jsonlPath)
	}

	return paths
}

// applyDeletionsFromMerge applies deletions discovered during 3-way merge
// This is the main entry point for deletion tracking during sync
func applyDeletionsFromMerge(ctx context.Context, store storage.Storage, jsonlPath string) error {
	merged, err := merge3WayAndPruneDeletions(ctx, store, jsonlPath)
	if err != nil {
		return err
	}

	if !merged {
		// No merge performed (no base snapshot), initialize for next time
		if err := initializeSnapshotsIfNeeded(jsonlPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize snapshots: %v\n", err)
		}
	}

	return nil
}
