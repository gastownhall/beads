package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/merge"
	"github.com/steveyegge/beads/internal/storage"
)

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

	// Prune accepted deletions from the database
	// Collect all deletion errors - fail the operation if any delete fails
	var deletionErrors []error
	for _, id := range acceptedDeletions {
		if err := store.DeleteIssue(ctx, id); err != nil {
			deletionErrors = append(deletionErrors, fmt.Errorf("issue %s: %w", id, err))
		}
	}

	if len(deletionErrors) > 0 {
		return false, fmt.Errorf("deletion failures (DB may be inconsistent): %v", deletionErrors)
	}

	// Print stats if deletions were found
	stats := sm.GetStats()
	if stats.DeletionsFound > 0 {
		fmt.Fprintf(os.Stderr, "3-way merge: pruned %d deleted issue(s) from database (base: %d, left: %d, merged: %d)\n",
			stats.DeletionsFound, stats.BaseCount, stats.LeftCount, stats.MergedCount)
	}

	return true, nil
}

// cleanupSnapshots removes the snapshot files and their metadata
// Deprecated: Use SnapshotManager.Cleanup() instead
func cleanupSnapshots(jsonlPath string) error {
	sm := NewSnapshotManager(jsonlPath)
	return sm.Cleanup()
}

// validateSnapshotConsistency checks if snapshot files are consistent
// Deprecated: Use SnapshotManager.Validate() instead
func validateSnapshotConsistency(jsonlPath string) error {
	sm := NewSnapshotManager(jsonlPath)
	return sm.Validate()
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
