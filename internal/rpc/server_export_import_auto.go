package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/autoimport"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/importer"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// handleExport handles the export operation
func (s *Server) handleExport(req *Request) Response {
	var exportArgs ExportArgs
	if err := json.Unmarshal(req.Args, &exportArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid export args: %v", err),
		}
	}

	store := s.storage

	ctx := s.reqCtx(req)

	// Get all issues
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get issues: %v", err),
		}
	}

	// Sort by ID for consistent output
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].ID < issues[j].ID
	})

	// Populate dependencies for all issues (avoid N+1)
	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get dependencies: %v", err),
		}
	}
	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
	}

	// Populate labels for all issues
	for _, issue := range issues {
		labels, err := store.GetLabels(ctx, issue.ID)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to get labels for %s: %v", issue.ID, err),
			}
		}
		issue.Labels = labels
	}

	// Populate comments for all issues
	for _, issue := range issues {
		comments, err := store.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to get comments for %s: %v", issue.ID, err),
			}
		}
		issue.Comments = comments
	}

	// Create temp file for atomic write
	dir := filepath.Dir(exportArgs.JSONLPath)
	base := filepath.Base(exportArgs.JSONLPath)
	tempFile, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to create temp file: %v", err),
		}
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
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to encode issue %s: %v", issue.ID, err),
			}
		}
		exportedIDs = append(exportedIDs, issue.ID)
	}

	// Close temp file before rename
	_ = tempFile.Close()

	// Atomic replace
	if err := os.Rename(tempPath, exportArgs.JSONLPath); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to replace JSONL file: %v", err),
		}
	}

	// Set appropriate file permissions (0600: rw-------)
	if err := os.Chmod(exportArgs.JSONLPath, 0600); err != nil {
		// Non-fatal, just log
		fmt.Fprintf(os.Stderr, "Warning: failed to set file permissions: %v\n", err)
	}

	// Clear dirty flags for exported issues
	if err := store.ClearDirtyIssuesByID(ctx, exportedIDs); err != nil {
		// Non-fatal, just log
		fmt.Fprintf(os.Stderr, "Warning: failed to clear dirty flags: %v\n", err)
	}

	result := map[string]interface{}{
		"exported_count": len(exportedIDs),
		"path":           exportArgs.JSONLPath,
	}
	data, _ := json.Marshal(result)
	return Response{
		Success: true,
		Data:    data,
	}
}

// handleImport handles the import operation
func (s *Server) handleImport(req *Request) Response {
	var importArgs ImportArgs
	if err := json.Unmarshal(req.Args, &importArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid import args: %v", err),
		}
	}

	// Note: The actual import logic is complex and lives in cmd/bd/import.go
	// For now, we'll return an error suggesting to use direct mode
	// In the future, we can refactor the import logic into a shared package
	return Response{
		Success: false,
		Error:   "import via daemon not yet implemented, use --no-daemon flag",
	}
}

// checkAndAutoImportIfStale checks if JSONL is newer than last import and triggers auto-import
// This fixes bd-132: daemon shows stale data after git pull
// This fixes bd-8931: daemon gets stuck when auto-import blocked by git conflicts
func (s *Server) checkAndAutoImportIfStale(req *Request) error {
	// Get storage for this request
	store := s.storage

	ctx := s.reqCtx(req)

	// Get database path from storage
	sqliteStore, ok := store.(*sqlite.SQLiteStorage)
	if !ok {
		return fmt.Errorf("storage is not SQLiteStorage")
	}
	dbPath := sqliteStore.Path()

	// Fast path: Check if JSONL is stale using cheap mtime check
	// This avoids reading/hashing JSONL on every request
	isStale, err := autoimport.CheckStaleness(ctx, store, dbPath)
	if err != nil {
		// Log error but allow request to proceed (don't block on staleness check failure)
		fmt.Fprintf(os.Stderr, "Warning: failed to check staleness: %v\n", err)
		return nil
	}
	if !isStale {
		return nil
	}

	// Single-flight guard: Only allow one import at a time
	// If import is already running, skip and let the request proceed (bd-8931)
	// This prevents blocking RPC requests when import is in progress
	if !s.importInProgress.CompareAndSwap(false, true) {
		return nil
	}

	// Track whether we should release the lock via defer
	// Set to false if we manually release early to avoid double-release bug
	shouldDeferRelease := true
	defer func() {
		if shouldDeferRelease {
			s.importInProgress.Store(false)
		}
	}()

	// Check if git has uncommitted changes that include beads files (bd-8931)
	// If JSONL files are uncommitted, skip auto-import to avoid conflicts
	// This prevents daemon corruption when workspace is dirty
	dbDir := filepath.Dir(dbPath)
	workspaceRoot := filepath.Dir(dbDir) // Go up from .beads to workspace root
	if hasUncommittedBeadsFiles(workspaceRoot) {
		// CRITICAL: Release lock and disable defer to avoid double-release race
		s.importInProgress.Store(false)
		shouldDeferRelease = false

		fmt.Fprintf(os.Stderr, "Warning: auto-import skipped - .beads files have uncommitted changes. Run 'bd sync' after committing.\n")
		return nil
	}

	// Double-check staleness after acquiring lock (another goroutine may have imported)
	isStale, err = autoimport.CheckStaleness(ctx, store, dbPath)
	if err != nil {
		// Log error but allow request to proceed (don't block on staleness check failure)
		fmt.Fprintf(os.Stderr, "Warning: failed to check staleness: %v\n", err)
		return nil
	}
	if !isStale {
		return nil
	}

	// Create timeout context for import operation (bd-8931, bd-1048)
	// This prevents daemon from hanging if import gets stuck
	// Use shorter timeout (5s) to ensure client doesn't timeout waiting for response
	// Client has 30s timeout, so import must complete well before that
	importCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Perform actual import with timeout protection
	notify := autoimport.NewStderrNotifier(debug.Enabled())

	importFunc := func(ctx context.Context, issues []*types.Issue) (created, updated int, idMapping map[string]string, err error) {
		// Use the importer package to perform the actual import
		result, err := importer.ImportIssues(ctx, dbPath, store, issues, importer.Options{
			RenameOnImport: true, // Auto-rename prefix mismatches
			// Note: SkipPrefixValidation is false by default, so we validate and rename
		})
		if err != nil {
			return 0, 0, nil, err
		}
		return result.Created, result.Updated, result.IDMapping, nil
	}

	onChanged := func(needsFullExport bool) {
		// When IDs are remapped, trigger export so JSONL reflects the new IDs
		if needsFullExport {
			// Use a goroutine to avoid blocking the import
			// But capture store reference before goroutine to ensure it's not closed
			go func(s *Server, store storage.Storage, dbPath string) {
				// Create independent context with timeout
				// Don't derive from importCtx as that may be canceled already
				exportCtx, exportCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer exportCancel()

				// Check if server is shutting down before attempting export
				s.mu.RLock()
				isShuttingDown := s.shutdown
				s.mu.RUnlock()

				if isShuttingDown {
					return // Skip export if daemon is shutting down
				}

				if err := s.triggerExport(exportCtx, store, dbPath); err != nil {
					// Check if error is due to closed database
					if strings.Contains(err.Error(), "closed") || strings.Contains(err.Error(), "shutdown") {
						// Expected during shutdown, don't log
						return
					}
					fmt.Fprintf(os.Stderr, "Warning: failed to export after auto-import: %v\n", err)
				}
			}(s, store, dbPath)
		}
	}

	// Perform import with timeout (still synchronous but won't hang forever)
	err = autoimport.AutoImportIfNewer(importCtx, store, dbPath, notify, importFunc, onChanged)
	if err != nil {
		if importCtx.Err() == context.DeadlineExceeded {
			fmt.Fprintf(os.Stderr, "Error: auto-import timed out after 5s. Run 'bd sync --import-only' manually.\n")
			return fmt.Errorf("auto-import timed out")
		}
		// Log but don't fail the request - let it proceed with stale data
		fmt.Fprintf(os.Stderr, "Warning: auto-import failed: %v\n", err)
	}

	return nil
}

// hasUncommittedBeadsFiles checks if .beads directory has uncommitted changes
// Returns true only if beads-specific files (.jsonl) are uncommitted
// This is more targeted than checking all git changes, avoiding false positives
func hasUncommittedBeadsFiles(workspacePath string) bool {
	// Run git status --porcelain with timeout to check for uncommitted changes
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", ".beads/")
	cmd.Dir = workspacePath
	output, err := cmd.Output()
	if err != nil {
		// If git command fails, assume not in git repo or .beads not tracked
		// In this case, allow auto-import to proceed
		return false
	}

	// Parse git porcelain output format: "XY filename"
	// Where X is index status, Y is worktree status
	// Status codes: M (modified), A (added), D (deleted), R (renamed), etc.
	// See: https://git-scm.com/docs/git-status#_short_format
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if len(line) < 3 {
			continue // Need at least "XY " before filename
		}

		// Extract filename (starts at position 3, after status codes and space)
		filename := line[3:]

		// Check if this is a .jsonl file (the tracked data file)
		// Use filepath.Ext to properly check extension
		if filepath.Ext(filename) == ".jsonl" {
			// Check if file has uncommitted changes (not just untracked)
			// Index status (position 0) or worktree status (position 1)
			indexStatus := line[0]
			worktreeStatus := line[1]

			// Ignore untracked files (??), we only care about tracked files with changes
			if indexStatus == '?' && worktreeStatus == '?' {
				continue
			}

			// File is tracked and has uncommitted changes
			if indexStatus != ' ' || worktreeStatus != ' ' {
				return true
			}
		}
	}

	return false
}

// triggerExport exports all issues to JSONL after auto-import remaps IDs
// CRITICAL: Must populate all issue data (deps, labels, comments) to prevent data loss
func (s *Server) triggerExport(ctx context.Context, store storage.Storage, dbPath string) error {
	// Find JSONL path using database directory
	dbDir := filepath.Dir(dbPath)
	pattern := filepath.Join(dbDir, "*.jsonl")
	matches, err := filepath.Glob(pattern)
	var jsonlPath string
	if err == nil && len(matches) > 0 {
		jsonlPath = matches[0]
	} else {
		jsonlPath = filepath.Join(dbDir, "issues.jsonl")
	}

	// Get all issues from storage
	sqliteStore, ok := store.(*sqlite.SQLiteStorage)
	if !ok {
		return fmt.Errorf("storage is not SQLiteStorage")
	}

	// Export to JSONL (this will update the file with remapped IDs)
	allIssues, err := sqliteStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("failed to fetch issues for export: %w", err)
	}

	// Sort by ID for consistent output (same as handleExport)
	sort.Slice(allIssues, func(i, j int) bool {
		return allIssues[i].ID < allIssues[j].ID
	})

	// CRITICAL: Populate all related data to prevent data loss
	// This mirrors the logic in handleExport (lines 50-83)

	// Populate dependencies for all issues (avoid N+1 queries)
	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to get dependencies: %w", err)
	}
	for _, issue := range allIssues {
		issue.Dependencies = allDeps[issue.ID]
	}

	// Populate labels for all issues
	for _, issue := range allIssues {
		labels, err := store.GetLabels(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("failed to get labels for %s: %w", issue.ID, err)
		}
		issue.Labels = labels
	}

	// Populate comments for all issues
	for _, issue := range allIssues {
		comments, err := store.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("failed to get comments for %s: %w", issue.ID, err)
		}
		issue.Comments = comments
	}

	// Write to JSONL file with atomic replace (temp file + rename)
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

	encoder := json.NewEncoder(tempFile)
	for _, issue := range allIssues {
		if err := encoder.Encode(issue); err != nil {
			return fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
	}

	// Close temp file before rename
	_ = tempFile.Close()

	// Atomic replace
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		return fmt.Errorf("failed to replace JSONL file: %w", err)
	}

	return nil
}
