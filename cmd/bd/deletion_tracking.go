package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/steveyegge/beads/internal/merge"
	"github.com/steveyegge/beads/internal/storage"
)

// jsonEquals compares two JSON strings semantically, handling field reordering
func jsonEquals(a, b string) bool {
	var objA, objB map[string]interface{}
	if err := json.Unmarshal([]byte(a), &objA); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(b), &objB); err != nil {
		return false
	}
	return reflect.DeepEqual(objA, objB)
}

// getSnapshotPaths returns paths for base and left snapshot files
func getSnapshotPaths(jsonlPath string) (basePath, leftPath string) {
	dir := filepath.Dir(jsonlPath)
	basePath = filepath.Join(dir, "beads.base.jsonl")
	leftPath = filepath.Join(dir, "beads.left.jsonl")
	return
}

// captureLeftSnapshot copies the current JSONL to the left snapshot file
// This should be called after export, before git pull
// Uses atomic file operations to prevent race conditions
func captureLeftSnapshot(jsonlPath string) error {
	_, leftPath := getSnapshotPaths(jsonlPath)
	// Use process-specific temp file to prevent concurrent write conflicts
	tempPath := fmt.Sprintf("%s.%d.tmp", leftPath, os.Getpid())
	if err := copyFileSnapshot(jsonlPath, tempPath); err != nil {
		return err
	}
	// Atomic rename on POSIX systems
	return os.Rename(tempPath, leftPath)
}

// updateBaseSnapshot copies the current JSONL to the base snapshot file
// This should be called after successful import to track the new baseline
// Uses atomic file operations to prevent race conditions
func updateBaseSnapshot(jsonlPath string) error {
	basePath, _ := getSnapshotPaths(jsonlPath)
	// Use process-specific temp file to prevent concurrent write conflicts
	tempPath := fmt.Sprintf("%s.%d.tmp", basePath, os.Getpid())
	if err := copyFileSnapshot(jsonlPath, tempPath); err != nil {
		return err
	}
	// Atomic rename on POSIX systems
	return os.Rename(tempPath, basePath)
}

// merge3WayAndPruneDeletions performs 3-way merge and prunes accepted deletions from DB
// Returns true if merge was performed, false if skipped (no base file)
func merge3WayAndPruneDeletions(ctx context.Context, store storage.Storage, jsonlPath string) (bool, error) {
	basePath, leftPath := getSnapshotPaths(jsonlPath)

	// If no base snapshot exists, skip deletion handling (first run or bootstrap)
	if !fileExists(basePath) {
		return false, nil
	}

	// Run 3-way merge: base (last import) vs left (pre-pull export) vs right (pulled JSONL)
	tmpMerged := jsonlPath + ".merged"
	// Ensure temp file cleanup on failure
	defer func() {
		if fileExists(tmpMerged) {
			os.Remove(tmpMerged)
		}
	}()

	err := merge.Merge3Way(tmpMerged, basePath, leftPath, jsonlPath, false)
	if err != nil {
		// Merge error (including conflicts) is returned as error
		return false, fmt.Errorf("3-way merge failed: %w", err)
	}

	// Replace the JSONL with merged result
	if err := os.Rename(tmpMerged, jsonlPath); err != nil {
		return false, fmt.Errorf("failed to replace JSONL with merged result: %w", err)
	}

	// Compute accepted deletions (issues in base but not in merged, and unchanged locally)
	acceptedDeletions, err := computeAcceptedDeletions(basePath, leftPath, jsonlPath)
	if err != nil {
		return false, fmt.Errorf("failed to compute accepted deletions: %w", err)
	}

	// Prune accepted deletions from the database
	// Use type assertion to access DeleteIssue method (available in concrete SQLiteStorage)
	type deleter interface {
		DeleteIssue(context.Context, string) error
	}

	d, ok := store.(deleter)
	if !ok {
		return false, fmt.Errorf("storage backend does not support DeleteIssue")
	}

	// Collect all deletion errors - fail the operation if any delete fails
	var deletionErrors []error
	for _, id := range acceptedDeletions {
		if err := d.DeleteIssue(ctx, id); err != nil {
			deletionErrors = append(deletionErrors, fmt.Errorf("issue %s: %w", id, err))
		}
	}

	if len(deletionErrors) > 0 {
		return false, fmt.Errorf("deletion failures (DB may be inconsistent): %v", deletionErrors)
	}

	if len(acceptedDeletions) > 0 {
		fmt.Fprintf(os.Stderr, "3-way merge: pruned %d deleted issue(s) from database\n", len(acceptedDeletions))
	}

	return true, nil
}

// computeAcceptedDeletions identifies issues that were deleted in the remote
// and should be removed from the local database.
//
// An issue is an "accepted deletion" if:
// - It exists in base (last import)
// - It does NOT exist in merged (after 3-way merge)
// - It is unchanged in left (pre-pull export) compared to base
//
// This means the issue was deleted remotely and we had no local modifications,
// so we should accept the deletion and prune it from our DB.
func computeAcceptedDeletions(basePath, leftPath, mergedPath string) ([]string, error) {
	// Build map of ID -> raw line for base and left
	baseIndex, err := buildIDToLineMap(basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read base snapshot: %w", err)
	}

	leftIndex, err := buildIDToLineMap(leftPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read left snapshot: %w", err)
	}

	// Build set of IDs in merged result
	mergedIDs, err := buildIDSet(mergedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read merged file: %w", err)
	}

	// Find accepted deletions
	var deletions []string
	for id, baseLine := range baseIndex {
		// Issue in base but not in merged
		if !mergedIDs[id] {
			// Check if unchanged locally using semantic JSON comparison
			if leftLine, existsInLeft := leftIndex[id]; existsInLeft && jsonEquals(leftLine, baseLine) {
				deletions = append(deletions, id)
			}
		}
	}

	return deletions, nil
}

// buildIDToLineMap reads a JSONL file and returns a map of issue ID -> raw JSON line
func buildIDToLineMap(path string) (map[string]string, error) {
	result := make(map[string]string)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil // Empty map for missing files
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Parse just the ID field
		var issue struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return nil, fmt.Errorf("failed to parse issue ID from line: %w", err)
		}

		result[issue.ID] = line
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// buildIDSet reads a JSONL file and returns a set of issue IDs
func buildIDSet(path string) (map[string]bool, error) {
	result := make(map[string]bool)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil // Empty set for missing files
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Parse just the ID field
		var issue struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return nil, fmt.Errorf("failed to parse issue ID from line: %w", err)
		}

		result[issue.ID] = true
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// copyFileSnapshot copies a file from src to dst (renamed to avoid conflict with migrate_hash_ids.go)
func copyFileSnapshot(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		return err
	}

	return destFile.Sync()
}

// cleanupSnapshots removes the snapshot files
// This is useful for cleanup after errors or manual operations
func cleanupSnapshots(jsonlPath string) error {
	basePath, leftPath := getSnapshotPaths(jsonlPath)
	
	_ = os.Remove(basePath)
	_ = os.Remove(leftPath)
	
	return nil
}

// validateSnapshotConsistency checks if snapshot files are consistent
// Returns an error if snapshots are corrupted or missing critical data
func validateSnapshotConsistency(jsonlPath string) error {
	basePath, leftPath := getSnapshotPaths(jsonlPath)

	// Base file is optional (might not exist on first run)
	if fileExists(basePath) {
		if _, err := buildIDSet(basePath); err != nil {
			return fmt.Errorf("base snapshot is corrupted: %w", err)
		}
	}

	// Left file is optional (might not exist if export hasn't run)
	if fileExists(leftPath) {
		if _, err := buildIDSet(leftPath); err != nil {
			return fmt.Errorf("left snapshot is corrupted: %w", err)
		}
	}

	return nil
}

// getSnapshotStats returns statistics about the snapshot files
func getSnapshotStats(jsonlPath string) (baseCount, leftCount int, baseExists, leftExists bool) {
	basePath, leftPath := getSnapshotPaths(jsonlPath)

	if baseIDs, err := buildIDSet(basePath); err == nil {
		baseExists = true
		baseCount = len(baseIDs)
	}

	if leftIDs, err := buildIDSet(leftPath); err == nil {
		leftExists = true
		leftCount = len(leftIDs)
	}

	return
}

// initializeSnapshotsIfNeeded creates initial snapshot files if they don't exist
// This is called during init or first sync to bootstrap the deletion tracking
func initializeSnapshotsIfNeeded(jsonlPath string) error {
	basePath, _ := getSnapshotPaths(jsonlPath)

	// If JSONL exists but base snapshot doesn't, create initial base
	if fileExists(jsonlPath) && !fileExists(basePath) {
		if err := copyFileSnapshot(jsonlPath, basePath); err != nil {
			return fmt.Errorf("failed to initialize base snapshot: %w", err)
		}
	}

	return nil
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
