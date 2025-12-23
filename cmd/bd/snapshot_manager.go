package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const (
	// MagicHeader is written to snapshot files for corruption detection
	MagicHeader = "# beads snapshot v1\n"

	// maxSnapshotAge is the maximum allowed age for a snapshot file (1 hour)
	maxSnapshotAge = 1 * time.Hour
)

// snapshotMetadata contains versioning info for snapshot files
type snapshotMetadata struct {
	Version   string    `json:"version"`   // bd version that created this snapshot
	Timestamp time.Time `json:"timestamp"` // When snapshot was created
	CommitSHA string    `json:"commit"`    // Git commit SHA at snapshot time
}

// SnapshotStats contains statistics about snapshot operations
type SnapshotStats struct {
	BaseCount      int  // Number of issues in base snapshot
	LeftCount      int  // Number of issues in left snapshot
	MergedCount    int  // Number of issues in merged result
	DeletionsFound int  // Number of deletions detected
	BaseExists     bool // Whether base snapshot exists
	LeftExists     bool // Whether left snapshot exists
}

// SnapshotManager handles snapshot file operations and validation
type SnapshotManager struct {
	jsonlPath string
	stats     SnapshotStats
}

// NewSnapshotManager creates a new snapshot manager for the given JSONL path
func NewSnapshotManager(jsonlPath string) *SnapshotManager {
	return &SnapshotManager{
		jsonlPath: jsonlPath,
		stats:     SnapshotStats{},
	}
}

// GetStats returns accumulated statistics about snapshot operations
func (sm *SnapshotManager) GetStats() SnapshotStats {
	return sm.stats
}

// getSnapshotPaths returns paths for base and left snapshot files
func (sm *SnapshotManager) getSnapshotPaths() (basePath, leftPath string) {
	dir := filepath.Dir(sm.jsonlPath)
	basePath = filepath.Join(dir, "beads.base.jsonl")
	leftPath = filepath.Join(dir, "beads.left.jsonl")
	return
}

// getSnapshotMetadataPaths returns paths for metadata files
func (sm *SnapshotManager) getSnapshotMetadataPaths() (baseMeta, leftMeta string) {
	dir := filepath.Dir(sm.jsonlPath)
	baseMeta = filepath.Join(dir, "beads.base.meta.json")
	leftMeta = filepath.Join(dir, "beads.left.meta.json")
	return
}

// CaptureLeft copies the current JSONL to the left snapshot file
// This should be called after export, before git pull
func (sm *SnapshotManager) CaptureLeft() error {
	_, leftPath := sm.getSnapshotPaths()
	_, leftMetaPath := sm.getSnapshotMetadataPaths()

	// Use process-specific temp file to prevent concurrent write conflicts
	tempPath := fmt.Sprintf("%s.%d.tmp", leftPath, os.Getpid())
	if err := sm.copyFile(sm.jsonlPath, tempPath); err != nil {
		return fmt.Errorf("failed to copy to temp file: %w", err)
	}

	// Atomic rename on POSIX systems
	if err := os.Rename(tempPath, leftPath); err != nil {
		return fmt.Errorf("failed to rename snapshot: %w", err)
	}

	// Write metadata
	meta := sm.createMetadata()
	if err := sm.writeMetadata(leftMetaPath, meta); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	// Update stats
	if ids, err := sm.buildIDSet(leftPath); err == nil {
		sm.stats.LeftExists = true
		sm.stats.LeftCount = len(ids)
	}

	return nil
}

// UpdateBase copies the current JSONL to the base snapshot file
// This should be called after successful import to track the new baseline
func (sm *SnapshotManager) UpdateBase() error {
	basePath, _ := sm.getSnapshotPaths()
	baseMetaPath, _ := sm.getSnapshotMetadataPaths()

	// Use process-specific temp file to prevent concurrent write conflicts
	tempPath := fmt.Sprintf("%s.%d.tmp", basePath, os.Getpid())
	if err := sm.copyFile(sm.jsonlPath, tempPath); err != nil {
		return fmt.Errorf("failed to copy to temp file: %w", err)
	}

	// Atomic rename on POSIX systems
	if err := os.Rename(tempPath, basePath); err != nil {
		return fmt.Errorf("failed to rename snapshot: %w", err)
	}

	// Write metadata
	meta := sm.createMetadata()
	if err := sm.writeMetadata(baseMetaPath, meta); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	// Update stats
	if ids, err := sm.buildIDSet(basePath); err == nil {
		sm.stats.BaseExists = true
		sm.stats.BaseCount = len(ids)
	}

	return nil
}

// Validate checks if snapshots exist and are valid
func (sm *SnapshotManager) Validate() error {
	basePath, leftPath := sm.getSnapshotPaths()
	baseMetaPath, leftMetaPath := sm.getSnapshotMetadataPaths()

	// Check if base snapshot exists
	if !fileExists(basePath) {
		return nil // No base snapshot - first run, not an error
	}

	currentCommit := getCurrentCommitSHA()

	// Validate base snapshot
	baseMeta, err := sm.readMetadata(baseMetaPath)
	if err != nil {
		return fmt.Errorf("base snapshot metadata error: %w", err)
	}

	if err := sm.validateMetadata(baseMeta, currentCommit); err != nil {
		return fmt.Errorf("base snapshot invalid: %w", err)
	}

	// Validate left snapshot if it exists
	if fileExists(leftPath) {
		leftMeta, err := sm.readMetadata(leftMetaPath)
		if err != nil {
			return fmt.Errorf("left snapshot metadata error: %w", err)
		}

		if err := sm.validateMetadata(leftMeta, currentCommit); err != nil {
			return fmt.Errorf("left snapshot invalid: %w", err)
		}
	}

	// Check for corruption
	if _, err := sm.buildIDSet(basePath); err != nil {
		return fmt.Errorf("base snapshot corrupted: %w", err)
	}

	if fileExists(leftPath) {
		if _, err := sm.buildIDSet(leftPath); err != nil {
			return fmt.Errorf("left snapshot corrupted: %w", err)
		}
	}

	return nil
}

// Cleanup removes all snapshot files and metadata
//
//nolint:unparam // error return kept for API consistency with other methods
func (sm *SnapshotManager) Cleanup() error {
	basePath, leftPath := sm.getSnapshotPaths()
	baseMetaPath, leftMetaPath := sm.getSnapshotMetadataPaths()

	// Best-effort cleanup of snapshot files (may not exist)
	_ = os.Remove(basePath)
	_ = os.Remove(leftPath)
	_ = os.Remove(baseMetaPath)
	_ = os.Remove(leftMetaPath)

	// Reset stats
	sm.stats = SnapshotStats{}

	return nil
}

// Initialize creates initial snapshot files if they don't exist
func (sm *SnapshotManager) Initialize() error {
	basePath, _ := sm.getSnapshotPaths()
	baseMetaPath, _ := sm.getSnapshotMetadataPaths()

	// If JSONL exists but base snapshot doesn't, create initial base
	if fileExists(sm.jsonlPath) && !fileExists(basePath) {
		if err := sm.copyFile(sm.jsonlPath, basePath); err != nil {
			return fmt.Errorf("failed to initialize base snapshot: %w", err)
		}

		// Create metadata
		meta := sm.createMetadata()
		if err := sm.writeMetadata(baseMetaPath, meta); err != nil {
			return fmt.Errorf("failed to initialize base snapshot metadata: %w", err)
		}

		// Update stats
		if ids, err := sm.buildIDSet(basePath); err == nil {
			sm.stats.BaseExists = true
			sm.stats.BaseCount = len(ids)
		}
	}

	return nil
}

// ComputeAcceptedDeletions identifies issues that were deleted remotely
// An issue is an "accepted deletion" if:
// - It exists in base (last import)
// - It does NOT exist in merged (after 3-way merge)
//
// Note (bd-pq5k): Deletion always wins over modification in the merge,
// so if an issue is deleted in the merged result, we accept it regardless
// of local changes.
func (sm *SnapshotManager) ComputeAcceptedDeletions(mergedPath string) ([]string, error) {
	basePath, _ := sm.getSnapshotPaths()

	// Build map of ID -> raw line for base
	baseIndex, err := sm.buildIDToLineMap(basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read base snapshot: %w", err)
	}

	// Build set of IDs in merged result
	mergedIDs, err := sm.buildIDSet(mergedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read merged file: %w", err)
	}

	sm.stats.MergedCount = len(mergedIDs)

	// Find accepted deletions
	var deletions []string
	for id := range baseIndex {
		// Issue in base but not in merged
		if !mergedIDs[id] {
			// bd-pq5k: Deletion always wins over modification in 3-way merge
			// If the merge resulted in deletion, accept it regardless of local changes
			// The 3-way merge already determined that deletion should win
			deletions = append(deletions, id)
		}
	}

	sm.stats.DeletionsFound = len(deletions)

	return deletions, nil
}

// BaseExists checks if the base snapshot exists
func (sm *SnapshotManager) BaseExists() bool {
	basePath, _ := sm.getSnapshotPaths()
	return fileExists(basePath)
}

// GetSnapshotPaths returns the base and left snapshot paths (exposed for testing)
func (sm *SnapshotManager) GetSnapshotPaths() (basePath, leftPath string) {
	return sm.getSnapshotPaths()
}

// BuildIDSet reads a JSONL file and returns a set of issue IDs (exposed for testing)
func (sm *SnapshotManager) BuildIDSet(path string) (map[string]bool, error) {
	return sm.buildIDSet(path)
}

// Private helper methods

func (sm *SnapshotManager) createMetadata() snapshotMetadata {
	return snapshotMetadata{
		Version:   getVersion(),
		Timestamp: time.Now(),
		CommitSHA: getCurrentCommitSHA(),
	}
}

func (sm *SnapshotManager) writeMetadata(path string, meta snapshotMetadata) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Use process-specific temp file for atomic write
	tempPath := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	// #nosec G306 -- metadata is shared across repo users and must stay readable
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write metadata temp file: %w", err)
	}

	// Atomic rename
	return os.Rename(tempPath, path)
}

func (sm *SnapshotManager) readMetadata(path string) (*snapshotMetadata, error) {
	// #nosec G304 -- metadata lives under .beads and path is derived internally
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No metadata file exists (backward compatibility)
		}
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	var meta snapshotMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	return &meta, nil
}

func (sm *SnapshotManager) validateMetadata(meta *snapshotMetadata, currentCommit string) error {
	if meta == nil {
		// No metadata file - likely old snapshot format, consider it stale
		return fmt.Errorf("snapshot has no metadata (stale format)")
	}

	// Check age
	age := time.Since(meta.Timestamp)
	if age > maxSnapshotAge {
		return fmt.Errorf("snapshot is too old (age: %v, max: %v)", age.Round(time.Second), maxSnapshotAge)
	}

	// Check version compatibility (major.minor must match)
	currentVersion := getVersion()
	if !isVersionCompatible(meta.Version, currentVersion) {
		return fmt.Errorf("snapshot version %s incompatible with current version %s", meta.Version, currentVersion)
	}

	// Check commit SHA if we're in a git repo
	if currentCommit != "" && meta.CommitSHA != "" && meta.CommitSHA != currentCommit {
		return fmt.Errorf("snapshot from different commit (snapshot: %s, current: %s)", meta.CommitSHA, currentCommit)
	}

	return nil
}

func (sm *SnapshotManager) buildIDToLineMap(path string) (map[string]string, error) {
	result := make(map[string]string)

	// #nosec G304 -- snapshot file lives in .beads/snapshots and path is derived internally
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

func (sm *SnapshotManager) buildIDSet(path string) (map[string]bool, error) {
	result := make(map[string]bool)

	// #nosec G304 -- snapshot file path derived from internal state
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

func (sm *SnapshotManager) jsonEquals(a, b string) bool {
	var objA, objB map[string]interface{}
	if err := json.Unmarshal([]byte(a), &objA); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(b), &objB); err != nil {
		return false
	}
	return reflect.DeepEqual(objA, objB)
}

func (sm *SnapshotManager) copyFile(src, dst string) error {
	// #nosec G304 -- snapshot copy only touches files inside .beads/snapshots
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	// #nosec G304 -- snapshot copy only writes files inside .beads/snapshots
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

// Package-level helper functions

func getCurrentCommitSHA() string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func isVersionCompatible(v1, v2 string) bool {
	// Extract major.minor from both versions
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	if len(parts1) < 2 || len(parts2) < 2 {
		return false
	}

	// Compare major.minor
	return parts1[0] == parts2[0] && parts1[1] == parts2[1]
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
