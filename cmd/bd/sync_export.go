package main

import (
	"bufio"
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

// Incremental export thresholds
const (
	// incrementalThreshold is the minimum total issue count to consider incremental export
	incrementalThreshold = 1000
	// incrementalDirtyRatio is the max ratio of dirty/total issues for incremental export
	// If more than 20% of issues are dirty, full export is likely faster
	incrementalDirtyRatio = 0.20
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

	// IssueContentHashes maps issue IDs to their content hashes (GH#1278)
	// Used to populate export_hashes table after successful export
	IssueContentHashes map[string]string
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

	// Update export_hashes for all exported issues (GH#1278)
	// This ensures child issues created with --parent are properly registered
	// for integrity tracking and incremental export detection.
	if len(result.IssueContentHashes) > 0 {
		for issueID, contentHash := range result.IssueContentHashes {
			if err := store.SetExportHash(ctx, issueID, contentHash); err != nil {
				// Non-fatal warning - continue with other issues
				fmt.Fprintf(os.Stderr, "Warning: failed to set export hash for %s: %v\n", issueID, err)
			}
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
	// Use store.Path() to get the actual database location, not the JSONL directory,
	// since sync-branch exports write JSONL to a worktree but the DB stays in the main repo.
	if result.JSONLPath != "" {
		if sqliteStore, ok := store.(*sqlite.SQLiteStorage); ok {
			dbPath := sqliteStore.Path()
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
//
// In dolt-native mode, this is a no-op - all sync happens via Dolt remotes.
func exportToJSONL(ctx context.Context, jsonlPath string) error {
	// Skip JSONL export in dolt-native mode (bd-zlih1)
	if store != nil && !ShouldExportJSONL(ctx, store) {
		return nil
	}

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
//
// In dolt-native mode, returns nil result - all sync happens via Dolt remotes.
func exportToJSONLDeferred(ctx context.Context, jsonlPath string) (*ExportResult, error) {
	// Skip JSONL export in dolt-native mode (bd-zlih1)
	if store != nil && !ShouldExportJSONL(ctx, store) {
		return nil, nil
	}

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

	// Write JSONL and collect content hashes (GH#1278)
	encoder := json.NewEncoder(tempFile)
	exportedIDs := make([]string, 0, len(issues))
	issueContentHashes := make(map[string]string, len(issues))
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			return nil, fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
		exportedIDs = append(exportedIDs, issue.ID)
		// Collect content hash for export_hashes table
		if issue.ContentHash != "" {
			issueContentHashes[issue.ID] = issue.ContentHash
		}
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
		JSONLPath:          jsonlPath,
		ExportedIDs:        exportedIDs,
		ContentHash:        contentHash,
		ExportTime:         exportTime,
		IssueContentHashes: issueContentHashes,
	}, nil
}

// exportToJSONLIncrementalDeferred performs incremental export for large repos.
// It checks if incremental export would be beneficial (large repo, few dirty issues),
// and if so, reads the existing JSONL, updates only dirty issues, and writes back.
// Falls back to full export when incremental is not beneficial.
//
// Returns the export result for deferred finalization (same as exportToJSONLDeferred).
//
// In dolt-native mode, returns nil result - all sync happens via Dolt remotes.
func exportToJSONLIncrementalDeferred(ctx context.Context, jsonlPath string) (*ExportResult, error) {
	// Skip JSONL export in dolt-native mode (bd-zlih1)
	if store != nil && !ShouldExportJSONL(ctx, store) {
		return nil, nil
	}

	// If daemon is running, delegate to it (daemon has its own optimization)
	if daemonClient != nil {
		return exportToJSONLDeferred(ctx, jsonlPath)
	}

	// Ensure store is initialized
	if err := ensureStoreActive(); err != nil {
		return nil, fmt.Errorf("failed to initialize store: %w", err)
	}

	// Check if incremental export would be beneficial
	useIncremental, dirtyIDs, err := shouldUseIncrementalExport(ctx, jsonlPath)
	if err != nil {
		// On error checking, fall back to full export
		return exportToJSONLDeferred(ctx, jsonlPath)
	}

	if !useIncremental {
		return exportToJSONLDeferred(ctx, jsonlPath)
	}

	// No dirty issues means nothing to export
	if len(dirtyIDs) == 0 {
		// Still need to return a valid result for idempotency
		contentHash, _ := computeJSONLHash(jsonlPath)
		return &ExportResult{
			JSONLPath:   jsonlPath,
			ExportedIDs: []string{},
			ContentHash: contentHash,
			ExportTime:  time.Now().Format(time.RFC3339Nano),
		}, nil
	}

	// Perform incremental export
	return performIncrementalExport(ctx, jsonlPath, dirtyIDs)
}

// shouldUseIncrementalExport determines if incremental export would be beneficial.
// Returns (useIncremental, dirtyIDs, error).
func shouldUseIncrementalExport(ctx context.Context, jsonlPath string) (bool, []string, error) {
	// Check if JSONL file exists (can't do incremental without existing file)
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		return false, nil, nil
	}

	// Get dirty issue IDs
	dirtyIDs, err := store.GetDirtyIssues(ctx)
	if err != nil {
		return false, nil, fmt.Errorf("failed to get dirty issues: %w", err)
	}

	// If no dirty issues, we can skip export entirely
	if len(dirtyIDs) == 0 {
		return true, dirtyIDs, nil
	}

	// Get total issue count from existing JSONL (fast line count)
	totalCount, err := countIssuesInJSONL(jsonlPath)
	if err != nil {
		// Can't read JSONL, fall back to full export
		return false, nil, nil
	}

	// Check thresholds:
	// 1. Total must be above threshold (small repos are fast enough with full export)
	// 2. Dirty ratio must be below threshold (if most issues changed, full export is faster)
	if totalCount < incrementalThreshold {
		return false, nil, nil
	}

	dirtyRatio := float64(len(dirtyIDs)) / float64(totalCount)
	if dirtyRatio > incrementalDirtyRatio {
		return false, nil, nil
	}

	return true, dirtyIDs, nil
}

// performIncrementalExport performs the actual incremental export.
// It reads the existing JSONL, queries only dirty issues, merges them,
// and writes the result.
func performIncrementalExport(ctx context.Context, jsonlPath string, dirtyIDs []string) (*ExportResult, error) {
	// Read existing JSONL into map[id]rawJSON
	issueMap, allIDs, err := readJSONLToMap(jsonlPath)
	if err != nil {
		// Fall back to full export on read error
		return exportToJSONLDeferred(ctx, jsonlPath)
	}

	// Query dirty issues from database and track which IDs were found
	// Also collect content hashes for export_hashes table (GH#1278)
	dirtyIssues := make([]*types.Issue, 0, len(dirtyIDs))
	issueByID := make(map[string]*types.Issue, len(dirtyIDs))
	issueContentHashes := make(map[string]string, len(dirtyIDs))
	for _, id := range dirtyIDs {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("failed to get dirty issue %s: %w", id, err)
		}
		issueByID[id] = issue // Store result (may be nil for deleted issues)
		if issue != nil {
			dirtyIssues = append(dirtyIssues, issue)
			if issue.ContentHash != "" {
				issueContentHashes[issue.ID] = issue.ContentHash
			}
		}
	}

	// Get dependencies for dirty issues only
	// Note: GetAllDependencyRecords is used because there's no batch method for specific IDs,
	// but for truly large repos this could be optimized with a targeted query
	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependencies: %w", err)
	}
	for _, issue := range dirtyIssues {
		issue.Dependencies = allDeps[issue.ID]
	}

	// Get labels for dirty issues (batch query)
	labelsMap, err := store.GetLabelsForIssues(ctx, dirtyIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	for _, issue := range dirtyIssues {
		issue.Labels = labelsMap[issue.ID]
	}

	// Get comments for dirty issues (batch query)
	commentsMap, err := store.GetCommentsForIssues(ctx, dirtyIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to get comments: %w", err)
	}
	for _, issue := range dirtyIssues {
		issue.Comments = commentsMap[issue.ID]
	}

	// Update map with dirty issues
	idSet := make(map[string]bool, len(allIDs))
	for _, id := range allIDs {
		idSet[id] = true
	}

	for _, issue := range dirtyIssues {
		// Skip wisps - they should never be exported
		if issue.Ephemeral {
			continue
		}

		// Serialize issue to JSON
		data, err := json.Marshal(issue)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal issue %s: %w", issue.ID, err)
		}

		issueMap[issue.ID] = data
		if !idSet[issue.ID] {
			allIDs = append(allIDs, issue.ID)
			idSet[issue.ID] = true
		}
	}

	// Handle tombstones and deletions using cached results (no second GetIssue call)
	for _, id := range dirtyIDs {
		issue := issueByID[id] // Use cached result
		if issue == nil {
			// Issue was fully deleted (not even a tombstone)
			delete(issueMap, id)
		} else if issue.Status == types.StatusTombstone {
			// Issue is a tombstone - keep it in export for propagation
			if !issue.Ephemeral {
				data, err := json.Marshal(issue)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal tombstone %s: %w", id, err)
				}
				issueMap[id] = data
			}
		}
	}

	// Build sorted list of IDs (excluding deleted ones)
	finalIDs := make([]string, 0, len(issueMap))
	for id := range issueMap {
		finalIDs = append(finalIDs, id)
	}
	slices.Sort(finalIDs)

	// Write to temp file, then atomic rename
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

	// Write JSONL in sorted order
	exportedIDs := make([]string, 0, len(finalIDs))
	for _, id := range finalIDs {
		data := issueMap[id]
		if _, err := tempFile.Write(data); err != nil {
			return nil, fmt.Errorf("failed to write issue %s: %w", id, err)
		}
		if _, err := tempFile.WriteString("\n"); err != nil {
			return nil, fmt.Errorf("failed to write newline: %w", err)
		}
		exportedIDs = append(exportedIDs, id)
	}

	// Close and rename
	_ = tempFile.Close()
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		return nil, fmt.Errorf("failed to replace JSONL file: %w", err)
	}

	// Set permissions
	if err := os.Chmod(jsonlPath, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to set file permissions: %v\n", err)
	}

	// Compute hash
	contentHash, _ := computeJSONLHash(jsonlPath)
	exportTime := time.Now().Format(time.RFC3339Nano)

	// Note: exportedIDs contains ALL IDs in the file, but we only need to clear
	// dirty flags for the dirtyIDs (which we received as parameter)
	return &ExportResult{
		JSONLPath:          jsonlPath,
		ExportedIDs:        dirtyIDs, // Only clear dirty flags for actually dirty issues
		ContentHash:        contentHash,
		ExportTime:         exportTime,
		IssueContentHashes: issueContentHashes,
	}, nil
}

// readJSONLToMap reads a JSONL file into a map of id -> raw JSON bytes.
// Also returns the list of IDs in original order.
func readJSONLToMap(jsonlPath string) (map[string]json.RawMessage, []string, error) {
	// #nosec G304 - controlled path
	file, err := os.Open(jsonlPath)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()

	issueMap := make(map[string]json.RawMessage)
	var ids []string

	scanner := bufio.NewScanner(file)
	// Use larger buffer for large lines
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Extract ID from JSON without full unmarshal
		var partial struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(line, &partial); err != nil {
			// Skip malformed lines
			continue
		}
		if partial.ID == "" {
			continue
		}

		// Store a copy of the line (scanner reuses buffer)
		lineCopy := make([]byte, len(line))
		copy(lineCopy, line)
		issueMap[partial.ID] = json.RawMessage(lineCopy)
		ids = append(ids, partial.ID)
	}

	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}

	return issueMap, ids, nil
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
