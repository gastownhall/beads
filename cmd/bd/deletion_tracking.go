package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// isIssueNotFoundError checks if the error indicates the issue doesn't exist in the database.
//
// During 3-way merge, we try to delete issues that were removed remotely. However, the issue
// may already be gone from the local database due to:
//   - Already deleted by a previous sync/import
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

// merge3WayAndPruneDeletions was the 3-way JSONL merge for deletion tracking.
// The 3-way merge engine has been removed (Dolt handles sync natively).
// This stub preserves the function signature for callers until JSONL sync is fully removed.
func merge3WayAndPruneDeletions(_ context.Context, _ *dolt.DoltStore, _ string) (bool, error) {
	return false, nil
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
func applyDeletionsFromMerge(ctx context.Context, store *dolt.DoltStore, jsonlPath string) error {
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
