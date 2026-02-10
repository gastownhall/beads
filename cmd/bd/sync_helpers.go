package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// getRepoKeyForPath extracts the stable repo identifier from a JSONL path.
// For single-repo mode, returns empty string (no suffix needed).
// For multi-repo mode, extracts the repo path (e.g., ".", "../frontend").
func getRepoKeyForPath(jsonlPath string) string {
	multiRepo := config.GetMultiRepoConfig()
	if multiRepo == nil {
		return "" // Single-repo mode
	}

	const suffix = "/.beads/issues.jsonl"
	if strings.HasSuffix(jsonlPath, suffix) {
		repoPath := strings.TrimSuffix(jsonlPath, suffix)

		primaryPath := multiRepo.Primary
		if primaryPath == "" {
			primaryPath = "."
		}
		if repoPath == primaryPath {
			return primaryPath
		}

		for _, additional := range multiRepo.Additional {
			if repoPath == additional {
				return additional
			}
		}
	}

	return ""
}

// sanitizeMetadataKey replaces characters that conflict with metadata key format.
func sanitizeMetadataKey(key string) string {
	return strings.ReplaceAll(key, ":", "_")
}

// getDebounceDuration returns the configured flush debounce duration.
func getDebounceDuration() time.Duration {
	duration := config.GetDuration("flush-debounce")
	if duration == 0 {
		return 5 * time.Second
	}
	return duration
}

// exportToJSONLWithStore exports issues to JSONL using the provided store.
// If multi-repo mode is configured, routes issues to their respective JSONL files.
// Otherwise, exports to a single JSONL file.
func exportToJSONLWithStore(ctx context.Context, store storage.Storage, jsonlPath string) error {
	// Try multi-repo export first
	if mrStore, ok := store.(storage.MultiRepoStorage); ok {
		results, err := mrStore.ExportToMultiRepo(ctx)
		if err != nil {
			return fmt.Errorf("multi-repo export failed: %w", err)
		}
		if results != nil {
			// Multi-repo mode active - export succeeded
			return nil
		}
	}

	// Single-repo mode - use existing logic
	// Get all issues including tombstones for sync propagation
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return fmt.Errorf("failed to get issues: %w", err)
	}

	// Safety check: prevent exporting empty database over non-empty JSONL
	if len(issues) == 0 {
		existingCount, err := countIssuesInJSONL(jsonlPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("warning: failed to read existing JSONL: %w", err)
			}
		} else if existingCount > 0 {
			return fmt.Errorf("refusing to export empty database over non-empty JSONL file (database: 0 issues, JSONL: %d issues). This would result in data loss", existingCount)
		}
	}

	// Sort by ID for consistent output
	slices.SortFunc(issues, func(a, b *types.Issue) int {
		return cmp.Compare(a.ID, b.ID)
	})

	// Populate dependencies for all issues
	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to get dependencies: %w", err)
	}
	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
	}

	// Populate labels for all issues
	for _, issue := range issues {
		labels, err := store.GetLabels(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("failed to get labels for %s: %w", issue.ID, err)
		}
		issue.Labels = labels
	}

	// Populate comments for all issues
	for _, issue := range issues {
		comments, err := store.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("failed to get comments for %s: %w", issue.ID, err)
		}
		issue.Comments = comments
	}

	// Create temp file for atomic write
	dir := filepath.Dir(jsonlPath)
	base := filepath.Base(jsonlPath)
	tempFile, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()

	// Use defer pattern for proper cleanup
	var writeErr error
	defer func() {
		_ = tempFile.Close()
		if writeErr != nil {
			_ = os.Remove(tempPath)
		}
	}()

	// Write JSONL
	for _, issue := range issues {
		data, marshalErr := json.Marshal(issue)
		if marshalErr != nil {
			writeErr = fmt.Errorf("failed to marshal issue %s: %w", issue.ID, marshalErr)
			return writeErr
		}
		if _, writeErr = tempFile.Write(data); writeErr != nil {
			writeErr = fmt.Errorf("failed to write issue %s: %w", issue.ID, writeErr)
			return writeErr
		}
		if _, writeErr = tempFile.WriteString("\n"); writeErr != nil {
			writeErr = fmt.Errorf("failed to write newline: %w", writeErr)
			return writeErr
		}
	}

	// Close before rename
	if writeErr = tempFile.Close(); writeErr != nil {
		writeErr = fmt.Errorf("failed to close temp file: %w", writeErr)
		return writeErr
	}

	// Atomic rename
	if writeErr = os.Rename(tempPath, jsonlPath); writeErr != nil {
		writeErr = fmt.Errorf("failed to rename temp file: %w", writeErr)
		return writeErr
	}

	return nil
}

// importToJSONLWithStore imports issues from JSONL using the provided store.
func importToJSONLWithStore(ctx context.Context, store storage.Storage, jsonlPath string) error {
	// Try multi-repo import first
	if mrStore, ok := store.(storage.MultiRepoStorage); ok {
		results, err := mrStore.HydrateFromMultiRepo(ctx)
		if err != nil {
			return fmt.Errorf("multi-repo import failed: %w", err)
		}
		if results != nil {
			// Multi-repo mode active - import succeeded
			return nil
		}
	}

	// Single-repo mode - use existing logic
	file, err := os.Open(jsonlPath) // #nosec G304 - controlled path from config
	if err != nil {
		return fmt.Errorf("failed to open JSONL: %w", err)
	}
	defer file.Close()

	var issues []*types.Issue
	scanner := bufio.NewScanner(file)
	const maxScannerBuffer = 1024 * 1024 // 1MB
	scanner.Buffer(make([]byte, 0, maxScannerBuffer), maxScannerBuffer)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse JSONL line %d: %v\n", lineNum, err)
			continue
		}
		issue.SetDefaults()

		// Migrate old JSONL format: auto-correct deleted status to tombstone
		if issue.Status == types.Status("deleted") && issue.DeletedAt != nil {
			issue.Status = types.StatusTombstone
		}

		// Fix: Any non-tombstone issue with deleted_at set is malformed
		if issue.Status != types.StatusTombstone && issue.DeletedAt != nil {
			issue.Status = types.StatusTombstone
		}

		if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
			now := time.Now()
			issue.ClosedAt = &now
		}

		// Ensure tombstones have deleted_at set
		if issue.Status == types.StatusTombstone && issue.DeletedAt == nil {
			now := time.Now()
			issue.DeletedAt = &now
		}

		issues = append(issues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read JSONL: %w", err)
	}

	opts := ImportOptions{
		DryRun:               false,
		SkipUpdate:           false,
		Strict:               false,
		SkipPrefixValidation: true,
	}

	_, err = importIssuesCore(ctx, "", store, issues, opts)
	return err
}

// updateExportMetadata updates jsonl_content_hash and related metadata after a successful export.
func updateExportMetadata(ctx context.Context, store storage.Storage, jsonlPath string, log *slog.Logger, keySuffix string) {
	if keySuffix != "" {
		keySuffix = sanitizeMetadataKey(keySuffix)
	}

	currentHash, err := computeJSONLHash(jsonlPath)
	if err != nil {
		log.Info("Warning: failed to compute JSONL hash for metadata update", "error", err)
		return
	}

	hashKey := "jsonl_content_hash"
	timeKey := "last_import_time"
	if keySuffix != "" {
		hashKey += ":" + keySuffix
		timeKey += ":" + keySuffix
	}

	if err := store.SetMetadata(ctx, hashKey, currentHash); err != nil {
		log.Info("Warning: failed to update metadata", "key", hashKey, "error", err)
		log.Info("Next export may require running 'bd import' first")
	}

	exportTime := time.Now().Format(time.RFC3339Nano)
	if err := store.SetMetadata(ctx, timeKey, exportTime); err != nil {
		log.Info("Warning: failed to update metadata", "key", timeKey, "error", err)
	}
}
