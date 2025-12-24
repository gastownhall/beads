package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

// exportToJSONL exports the database to JSONL format
func exportToJSONL(ctx context.Context, jsonlPath string) error {
	// If daemon is running, use RPC
	if daemonClient != nil {
		exportArgs := &rpc.ExportArgs{
			JSONLPath: jsonlPath,
		}
		resp, err := daemonClient.Export(exportArgs)
		if err != nil {
			return fmt.Errorf("daemon export failed: %w", err)
		}
		if !resp.Success {
			return fmt.Errorf("daemon export error: %s", resp.Error)
		}
		return nil
	}

	// Direct mode: access store directly
	// Ensure store is initialized
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}

	// Get all issues including tombstones for sync propagation (bd-rp4o fix)
	// Tombstones must be exported so they propagate to other clones and prevent resurrection
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return fmt.Errorf("failed to get issues: %w", err)
	}

	// Safety check: prevent exporting empty database over non-empty JSONL
	// Note: The main bd-53c protection is the reverse ZFC check earlier in sync.go
	// which runs BEFORE export. Here we only block the most catastrophic case (empty DB)
	// to allow legitimate deletions.
	if len(issues) == 0 {
		existingCount, countErr := countIssuesInJSONL(jsonlPath)
		if countErr != nil {
			// If we can't read the file, it might not exist yet, which is fine
			if !os.IsNotExist(countErr) {
				fmt.Fprintf(os.Stderr, "Warning: failed to read existing JSONL: %v\n", countErr)
			}
		} else if existingCount > 0 {
			return fmt.Errorf("refusing to export empty database over non-empty JSONL file (database: 0 issues, JSONL: %d issues)", existingCount)
		}
	}

	// Sort by ID for consistent output
	slices.SortFunc(issues, func(a, b *types.Issue) int {
		return cmp.Compare(a.ID, b.ID)
	})

	// Populate dependencies for all issues (avoid N+1)
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
	defer func() {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}()

	// Write JSONL
	encoder := json.NewEncoder(tempFile)
	exportedIDs := make([]string, 0, len(issues))
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			return fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
		exportedIDs = append(exportedIDs, issue.ID)
	}

	// Close temp file before rename (error checked implicitly by Rename success)
	_ = tempFile.Close()

	// Atomic replace
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		return fmt.Errorf("failed to replace JSONL file: %w", err)
	}

	// Set appropriate file permissions (0600: rw-------)
	if err := os.Chmod(jsonlPath, 0600); err != nil {
		// Non-fatal warning
		fmt.Fprintf(os.Stderr, "Warning: failed to set file permissions: %v\n", err)
	}

	// Clear dirty flags for exported issues
	if err := store.ClearDirtyIssuesByID(ctx, exportedIDs); err != nil {
		// Non-fatal warning
		fmt.Fprintf(os.Stderr, "Warning: failed to clear dirty flags: %v\n", err)
	}

	// Clear auto-flush state
	clearAutoFlushState()

	// Update jsonl_content_hash metadata to enable content-based staleness detection (bd-khnb fix)
	// After export, database and JSONL are in sync, so update hash to prevent unnecessary auto-import
	// Renamed from last_import_hash (bd-39o) - more accurate since updated on both import AND export
	if currentHash, err := computeJSONLHash(jsonlPath); err == nil {
		if err := store.SetMetadata(ctx, "jsonl_content_hash", currentHash); err != nil {
			// Non-fatal warning: Metadata update failures are intentionally non-fatal to prevent blocking
			// successful exports. System degrades gracefully to mtime-based staleness detection if metadata
			// is unavailable. This ensures export operations always succeed even if metadata storage fails.
			fmt.Fprintf(os.Stderr, "Warning: failed to update jsonl_content_hash: %v\n", err)
		}
		// Use RFC3339Nano for nanosecond precision to avoid race with file mtime (fixes #399)
		exportTime := time.Now().Format(time.RFC3339Nano)
		if err := store.SetMetadata(ctx, "last_import_time", exportTime); err != nil {
			// Non-fatal warning (see above comment about graceful degradation)
			fmt.Fprintf(os.Stderr, "Warning: failed to update last_import_time: %v\n", err)
		}
		// Note: mtime tracking removed in bd-v0y fix (git doesn't preserve mtime)
	}

	// Update database mtime to be >= JSONL mtime (fixes #278, #301, #321)
	// This prevents validatePreExport from incorrectly blocking on next export
	beadsDir := filepath.Dir(jsonlPath)
	dbPath := filepath.Join(beadsDir, "beads.db")
	if err := TouchDatabaseFile(dbPath, jsonlPath); err != nil {
		// Non-fatal warning
		fmt.Fprintf(os.Stderr, "Warning: failed to update database mtime: %v\n", err)
	}

	return nil
}
