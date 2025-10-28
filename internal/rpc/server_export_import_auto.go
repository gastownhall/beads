package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/steveyegge/beads/internal/autoimport"
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
	if err != nil || !isStale {
		return err
	}
	
	// Single-flight guard: Only allow one import at a time
	// If import is already running, skip and let the request proceed
	if !s.importInProgress.CompareAndSwap(false, true) {
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: auto-import already in progress, skipping\n")
		}
		return nil
	}
	defer s.importInProgress.Store(false)
	
	// Double-check staleness after acquiring lock (another goroutine may have imported)
	isStale, err = autoimport.CheckStaleness(ctx, store, dbPath)
	if err != nil || !isStale {
		return err
	}
	
	if os.Getenv("BD_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "Debug: daemon detected stale JSONL, auto-importing...\n")
	}
	
	// Perform actual import
	notify := autoimport.NewStderrNotifier(os.Getenv("BD_DEBUG") != "")
	
	importFunc := func(ctx context.Context, issues []*types.Issue) (created, updated int, idMapping map[string]string, err error) {
		// Use the importer package to perform the actual import
		result, err := importer.ImportIssues(ctx, dbPath, store, issues, importer.Options{
			ResolveCollisions: false, // Do NOT resolve collisions - update existing issues by ID
			RenameOnImport:    true,  // Auto-rename prefix mismatches
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
			go func() {
				if err := s.triggerExport(ctx, store, dbPath); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to export after auto-import: %v\n", err)
				}
			}()
		}
	}
	
	return autoimport.AutoImportIfNewer(ctx, store, dbPath, notify, importFunc, onChanged)
}

// triggerExport exports all issues to JSONL after auto-import remaps IDs
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

	// Write to JSONL file
	// Note: We reuse the export logic from the daemon's existing export mechanism
	// For now, this is a simple implementation - could be refactored to share with cmd/bd
	file, err := os.Create(jsonlPath) // #nosec G304 - controlled path from config
	if err != nil {
		return fmt.Errorf("failed to create JSONL file: %w", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	for _, issue := range allIssues {
		if err := encoder.Encode(issue); err != nil {
			return fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
	}

	return nil
}
