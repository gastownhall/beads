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

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/validation"
)

// ExportResult contains information needed to finalize an export after git commit.
// This enables atomic sync by deferring metadata updates until after git commit succeeds.
// See GH#885 for the atomicity gap this fixes.
type ExportResult struct {
	// JSONLPath is the path to the exported JSONL file
	JSONLPath string

	// ExportedIDs are the issue IDs that were exported
	ExportedIDs []string

	// ContentHash is the hash of the exported JSONL content
	ContentHash string

	// ExportTime is when the export was performed (RFC3339Nano format)
	ExportTime string
}

// finalizeExport updates SQLite metadata after a successful git commit.
// This is the second half of atomic sync - it marks the export as complete
// only after the git commit succeeds. If git commit fails, the metadata
// remains unchanged so the system knows the sync is incomplete.
// See GH#885 for the atomicity gap this fixes.
func finalizeExport(ctx context.Context, result *ExportResult) {
	if result == nil {
		return
	}

	// Ensure store is initialized
	if err := ensureStoreActive(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize store for finalize: %v\n", err)
		return
	}

	// Clear dirty flags for exported issues
	if len(result.ExportedIDs) > 0 {
		if err := store.ClearDirtyIssuesByID(ctx, result.ExportedIDs); err != nil {
			// Non-fatal warning
			fmt.Fprintf(os.Stderr, "Warning: failed to clear dirty flags: %v\n", err)
		}
	}

	// Clear auto-flush state
	clearAutoFlushState()

	// Update jsonl_content_hash metadata to enable content-based staleness detection
	if result.ContentHash != "" {
		if err := store.SetMetadata(ctx, "jsonl_content_hash", result.ContentHash); err != nil {
			// Non-fatal warning: Metadata update failures are intentionally non-fatal to prevent blocking
			// successful exports. System degrades gracefully to mtime-based staleness detection if metadata
			// is unavailable. This ensures export operations always succeed even if metadata storage fails.
			fmt.Fprintf(os.Stderr, "Warning: failed to update jsonl_content_hash: %v\n", err)
		}
		// Also update jsonl_file_hash for integrity validation (bd-160)
		// This ensures validateJSONLIntegrity() won't see a hash mismatch after
		// bd sync --flush-only runs (e.g., from pre-commit hook).
		if err := store.SetJSONLFileHash(ctx, result.ContentHash); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to update jsonl_file_hash: %v\n", err)
		}
	}

	// Update last_import_time
	if result.ExportTime != "" {
		if err := store.SetMetadata(ctx, "last_import_time", result.ExportTime); err != nil {
			// Non-fatal warning (see above comment about graceful degradation)
			fmt.Fprintf(os.Stderr, "Warning: failed to update last_import_time: %v\n", err)
		}
	}

	// Update database mtime to be >= JSONL mtime (fixes #278, #301, #321)
	// This prevents validatePreExport from incorrectly blocking on next export.
	//
	// Dolt backend does not use a SQLite DB file, so this check is SQLite-only.
	if result.JSONLPath != "" {
		if _, ok := store.(*sqlite.SQLiteStorage); ok {
			beadsDir := filepath.Dir(result.JSONLPath)
			dbPath := filepath.Join(beadsDir, "beads.db")
			if err := TouchDatabaseFile(dbPath, result.JSONLPath); err != nil {
				// Non-fatal warning
				fmt.Fprintf(os.Stderr, "Warning: failed to update database mtime: %v\n", err)
			}
		}
	}
}

// exportToJSONL exports the database to JSONL format.
// This is a convenience wrapper that exports and immediately finalizes.
// For atomic sync operations, use exportToJSONLDeferred + finalizeExport.
func exportToJSONL(ctx context.Context, jsonlPath string) error {
	result, err := exportToJSONLDeferred(ctx, jsonlPath)
	if err != nil {
		return err
	}
	// Immediately finalize for backward compatibility
	finalizeExport(ctx, result)
	return nil
}

// exportToJSONLDeferred exports the database to JSONL format but does NOT update
// SQLite metadata. The caller must call finalizeExport() after git commit succeeds.
// This enables atomic sync where metadata is only updated after git commit.
// See GH#885 for the atomicity gap this fixes.
func exportToJSONLDeferred(ctx context.Context, jsonlPath string) (*ExportResult, error) {
	// If daemon is running, use RPC
	// Note: daemon already handles its own metadata updates
	if daemonClient != nil {
		exportArgs := &rpc.ExportArgs{
			JSONLPath: jsonlPath,
		}
		resp, err := daemonClient.Export(exportArgs)
		if err != nil {
			return nil, fmt.Errorf("daemon export failed: %w", err)
		}
		if !resp.Success {
			return nil, fmt.Errorf("daemon export error: %s", resp.Error)
		}
		// Daemon handles its own metadata updates, return nil result
		return nil, nil
	}

	// Direct mode: access store directly
	// Ensure store is initialized
	if err := ensureStoreActive(); err != nil {
		return nil, fmt.Errorf("failed to initialize store: %w", err)
	}

	// Get all issues including tombstones for sync propagation (bd-rp4o fix)
	// Tombstones must be exported so they propagate to other clones and prevent resurrection
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return nil, fmt.Errorf("failed to get issues: %w", err)
	}

	// Safety check: prevent exporting empty database over non-empty JSONL
	// This blocks the catastrophic case where an empty/corrupted DB would overwrite
	// a valid JSONL. For staleness handling, use --pull-first which provides
	// structural protection via 3-way merge.
	if len(issues) == 0 {
		existingCount, countErr := countIssuesInJSONL(jsonlPath)
		if countErr != nil {
			// If we can't read the file, it might not exist yet, which is fine
			if !os.IsNotExist(countErr) {
				fmt.Fprintf(os.Stderr, "Warning: failed to read existing JSONL: %v\n", countErr)
			}
		} else if existingCount > 0 {
			return nil, fmt.Errorf("refusing to export empty database over non-empty JSONL file (database: 0 issues, JSONL: %d issues)", existingCount)
		}
	}

	// Filter out wisps - they should never be exported to JSONL
	// Wisps exist only in SQLite and are shared via .beads/redirect, not JSONL.
	// This prevents "zombie" issues that resurrect after mol squash deletes them.
	filteredIssues := make([]*types.Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.Ephemeral {
			continue
		}
		filteredIssues = append(filteredIssues, issue)
	}
	issues = filteredIssues

	// Sort by ID for consistent output
	slices.SortFunc(issues, func(a, b *types.Issue) int {
		return cmp.Compare(a.ID, b.ID)
	})

	// Populate dependencies for all issues (avoid N+1)
	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependencies: %w", err)
	}
	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
	}

	// Populate labels for all issues
	for _, issue := range issues {
		labels, err := store.GetLabels(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get labels for %s: %w", issue.ID, err)
		}
		issue.Labels = labels
	}

	// Populate comments for all issues
	for _, issue := range issues {
		comments, err := store.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get comments for %s: %w", issue.ID, err)
		}
		issue.Comments = comments
	}

	// Create temp file for atomic write
	dir := filepath.Dir(jsonlPath)
	base := filepath.Base(jsonlPath)
	tempFile, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
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
			return nil, fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
		exportedIDs = append(exportedIDs, issue.ID)
	}

	// Close temp file before rename (error checked implicitly by Rename success)
	_ = tempFile.Close()

	// Atomic replace
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		return nil, fmt.Errorf("failed to replace JSONL file: %w", err)
	}

	// Set appropriate file permissions (0600: rw-------)
	if err := os.Chmod(jsonlPath, 0600); err != nil {
		// Non-fatal warning
		fmt.Fprintf(os.Stderr, "Warning: failed to set file permissions: %v\n", err)
	}

	// Compute hash and time for the result (but don't update metadata yet)
	contentHash, _ := computeJSONLHash(jsonlPath)
	exportTime := time.Now().Format(time.RFC3339Nano)

	return &ExportResult{
		JSONLPath:   jsonlPath,
		ExportedIDs: exportedIDs,
		ContentHash: contentHash,
		ExportTime:  exportTime,
	}, nil
}

// validateOpenIssuesForSync validates all open issues against their templates
// before export, based on the validation.on-sync config setting.
// Returns an error if validation.on-sync is "error" and issues fail validation.
// Prints warnings if validation.on-sync is "warn".
// Does nothing if validation.on-sync is "none" (default).
func validateOpenIssuesForSync(ctx context.Context) error {
	validationMode := config.GetString("validation.on-sync")
	if validationMode == "none" || validationMode == "" {
		return nil
	}

	// Ensure store is active
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("failed to initialize store for validation: %w", err)
	}

	// Get all issues (excluding tombstones) and filter to open ones
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("failed to get issues for validation: %w", err)
	}

	// Filter to only open issues (not closed, not tombstones)
	var issues []*types.Issue
	for _, issue := range allIssues {
		if issue.Status != types.StatusClosed && issue.Status != types.StatusTombstone {
			issues = append(issues, issue)
		}
	}

	// Validate each issue
	var warnings []string
	for _, issue := range issues {
		if err := validation.LintIssue(issue); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", issue.ID, err))
		}
	}

	if len(warnings) == 0 {
		return nil
	}

	// Report based on mode
	if validationMode == "error" {
		fmt.Fprintf(os.Stderr, "%s Validation failed for %d issue(s):\n", ui.RenderFail("✗"), len(warnings))
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "  - %s\n", w)
		}
		return fmt.Errorf("template validation failed: %d issues missing required sections (set validation.on-sync: none or warn to proceed)", len(warnings))
	}

	// warn mode: print warnings but proceed
	fmt.Fprintf(os.Stderr, "%s Validation warnings for %d issue(s):\n", ui.RenderWarn("⚠"), len(warnings))
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "  - %s\n", w)
	}

	return nil
}
