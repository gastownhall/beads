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

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/syncbranch"
	"github.com/steveyegge/beads/internal/types"
)

// warnIfSyncBranchMisconfigured logs a warning at daemon startup if sync-branch
// equals the current branch. This is a one-time warning to alert users about
// the misconfiguration. The daemon continues to start (warn only, don't block).
// Returns true if misconfigured (warning was logged), false otherwise.
// GH#1258: Prevents silent failure when sync-branch == current-branch.
func warnIfSyncBranchMisconfigured(ctx context.Context, store storage.Storage, log *slog.Logger) bool {
	syncBranch, err := syncbranch.Get(ctx, store)
	if err != nil || syncBranch == "" {
		return false // No sync branch configured, not misconfigured
	}

	if syncbranch.IsSyncBranchSameAsCurrent(ctx, syncBranch) {
		log.Warn("sync-branch misconfiguration detected",
			"sync_branch", syncBranch,
			"message", "sync-branch is your current branch; daemon sync operations will be skipped; configure a dedicated sync branch (e.g., 'beads-sync') to enable sync")
		return true
	}

	return false
}

// shouldSkipDueToSameBranch checks if operation should be skipped because
// sync-branch == current-branch. Returns true if should skip, logs reason.
// Uses fail-open pattern: if branch detection fails, allows operation to proceed.
func shouldSkipDueToSameBranch(ctx context.Context, store storage.Storage, operation string, log *slog.Logger) bool {
	syncBranch, err := syncbranch.Get(ctx, store)
	if err != nil || syncBranch == "" {
		return false // No sync branch configured, allow
	}

	if syncbranch.IsSyncBranchSameAsCurrent(ctx, syncBranch) {
		log.Info("Skipping operation: sync-branch is current branch", "operation", operation, "sync_branch", syncBranch)
		return true
	}

	return false
}

// exportToJSONLWithStore exports issues to JSONL using the provided store.
// If multi-repo mode is configured, routes issues to their respective JSONL files.
// Otherwise, exports to a single JSONL file.
// Respects sync mode: skips JSONL export in dolt-native mode (bd-u9yv).
func exportToJSONLWithStore(ctx context.Context, store storage.Storage, jsonlPath string) error {
	// Check sync mode before JSONL export (bd-u9yv: dolt-native mode should skip JSONL)
	if !ShouldExportJSONL(ctx, store) {
		debug.Logf("skipping JSONL export (dolt-native mode)")
		return nil
	}

	// Try multi-repo export first
	sqliteStore, ok := store.(*sqlite.SQLiteStorage)
	if ok {
		results, err := sqliteStore.ExportToMultiRepo(ctx)
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
	// Tombstones must be exported so they propagate to other clones and prevent resurrection
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return fmt.Errorf("failed to get issues: %w", err)
	}

	// Safety check: prevent exporting empty database over non-empty JSONL
	// Note: The main protection is in sync.go's reverse ZFC check which runs BEFORE export.
	// Here we only block the most catastrophic case (empty DB) to allow legitimate deletions.
	if len(issues) == 0 {
		existingCount, err := countIssuesInJSONL(jsonlPath)
		if err != nil {
			// If we can't read the file, it might not exist yet, which is fine
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
			_ = os.Remove(tempPath) // Remove temp file on error
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

	// Update export_hashes for all exported issues (GH#1278)
	// This ensures child issues created with --parent are properly registered
	for _, issue := range issues {
		if issue.ContentHash != "" {
			if err := store.SetExportHash(ctx, issue.ID, issue.ContentHash); err != nil {
				// Non-fatal warning - continue with other issues
				fmt.Fprintf(os.Stderr, "Warning: failed to set export hash for %s: %v\n", issue.ID, err)
			}
		}
	}

	return nil
}

// importToJSONLWithStore imports issues from JSONL using the provided store
func importToJSONLWithStore(ctx context.Context, store storage.Storage, jsonlPath string) error {
	// Try multi-repo import first
	sqliteStore, ok := store.(*sqlite.SQLiteStorage)
	if ok {
		results, err := sqliteStore.HydrateFromMultiRepo(ctx)
		if err != nil {
			return fmt.Errorf("multi-repo import failed: %w", err)
		}
		if results != nil {
			// Multi-repo mode active - import succeeded
			return nil
		}
	}

	// Single-repo mode - use existing logic
	// Read JSONL file
	file, err := os.Open(jsonlPath) // #nosec G304 - controlled path from config
	if err != nil {
		return fmt.Errorf("failed to open JSONL: %w", err)
	}
	defer file.Close()

	// Parse all issues
	var issues []*types.Issue
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip empty lines
		if line == "" {
			continue
		}

		// Parse JSON
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			// Log error but continue - don't fail entire import
			fmt.Fprintf(os.Stderr, "Warning: failed to parse JSONL line %d: %v\n", lineNum, err)
			continue
		}
		issue.SetDefaults() // Apply defaults for omitted fields

		// Migrate old JSONL format: auto-correct deleted status to tombstone
		// This handles JSONL files from versions that used "deleted" instead of "tombstone"
		// (GH#1223: Stuck in sync diversion loop)
		if issue.Status == types.Status("deleted") && issue.DeletedAt != nil {
			issue.Status = types.StatusTombstone
		}

		// Fix: Any non-tombstone issue with deleted_at set is malformed and should be tombstone
		// This catches issues that may have been corrupted or migrated incorrectly
		if issue.Status != types.StatusTombstone && issue.DeletedAt != nil {
			issue.Status = types.StatusTombstone
		}

		if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
			now := time.Now()
			issue.ClosedAt = &now
		}

		// Ensure tombstones have deleted_at set (fix for malformed data)
		if issue.Status == types.StatusTombstone && issue.DeletedAt == nil {
			now := time.Now()
			issue.DeletedAt = &now
		}

		issues = append(issues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read JSONL: %w", err)
	}

	// Use existing import logic with auto-conflict resolution
	opts := ImportOptions{
		DryRun:               false,
		SkipUpdate:           false,
		Strict:               false,
		SkipPrefixValidation: true, // Skip prefix validation for auto-import
	}

	_, err = importIssuesCore(ctx, "", store, issues, opts)
	return err
}

// getRepoKeyForPath extracts the stable repo identifier from a JSONL path.
// For single-repo mode, returns empty string (no suffix needed).
// For multi-repo mode, extracts the repo path (e.g., ".", "../frontend").
// This creates portable metadata keys that work across different machine paths.
func getRepoKeyForPath(jsonlPath string) string {
	multiRepo := config.GetMultiRepoConfig()
	if multiRepo == nil {
		return "" // Single-repo mode
	}

	// Normalize the jsonlPath for comparison
	// Remove trailing "/.beads/issues.jsonl" to get repo path
	const suffix = "/.beads/issues.jsonl"
	if strings.HasSuffix(jsonlPath, suffix) {
		repoPath := strings.TrimSuffix(jsonlPath, suffix)

		// Try to match against primary repo
		primaryPath := multiRepo.Primary
		if primaryPath == "" {
			primaryPath = "."
		}
		if repoPath == primaryPath {
			return primaryPath
		}

		// Try to match against additional repos
		for _, additional := range multiRepo.Additional {
			if repoPath == additional {
				return additional
			}
		}
	}

	// Fallback: return empty string for single-repo mode behavior
	return ""
}

// sanitizeMetadataKey removes or replaces characters that conflict with metadata key format.
// On Windows, absolute paths contain colons (e.g., C:\...) which conflict with the ':' separator
// used in multi-repo metadata keys. This function replaces colons with underscores to make
// paths safe for use as metadata key suffixes.
func sanitizeMetadataKey(key string) string {
	return strings.ReplaceAll(key, ":", "_")
}

// updateExportMetadata updates jsonl_content_hash and related metadata after a successful export.
// This prevents "JSONL content has changed since last import" errors on subsequent exports.
// In multi-repo mode, keySuffix should be the stable repo identifier (e.g., ".", "../frontend").
//
// Metadata key format:
//   - Single-repo mode: "jsonl_content_hash", "last_import_time"
//   - Multi-repo mode: "jsonl_content_hash:<repo_key>", "last_import_time:<repo_key>", etc.
//     where <repo_key> is a stable repo identifier like "." or "../frontend"
//   - Windows paths: Colons in absolute paths (e.g., C:\...) are replaced with underscores
//   - Note: "last_import_mtime" was removed (git doesn't preserve mtime)
//   - Note: "last_import_hash" renamed to "jsonl_content_hash" - more accurate name
//
// Transaction boundaries:
// This function does NOT provide atomicity between JSONL write, metadata updates, and DB mtime.
// If a crash occurs between these operations, metadata may be inconsistent. However, this is
// acceptable because:
//  1. The worst case is "JSONL content has changed" error on next export
//  2. User can fix by running 'bd import' (safe, no data loss)
//  3. Current approach is simple and doesn't require complex WAL or format changes
//
// Future: Consider defensive checks on startup if this becomes a common issue.
func updateExportMetadata(ctx context.Context, store storage.Storage, jsonlPath string, log *slog.Logger, keySuffix string) {
	// Sanitize keySuffix to handle Windows paths with colons
	if keySuffix != "" {
		keySuffix = sanitizeMetadataKey(keySuffix)
	}

	currentHash, err := computeJSONLHash(jsonlPath)
	if err != nil {
		log.Info("Warning: failed to compute JSONL hash for metadata update", "error", err)
		return
	}

	// Build metadata keys with optional suffix for per-repo tracking
	// Renamed from last_import_hash to jsonl_content_hash
	hashKey := "jsonl_content_hash"
	timeKey := "last_import_time"
	if keySuffix != "" {
		hashKey += ":" + keySuffix
		timeKey += ":" + keySuffix
	}

	// Note: Metadata update failures are treated as warnings, not errors.
	// This is acceptable because the worst case is the next export will require
	// an import first, which is safe and prevents data loss.
	// Alternative: Make this critical and fail the export if metadata updates fail,
	// but this makes exports more fragile and doesn't prevent data corruption.
	if err := store.SetMetadata(ctx, hashKey, currentHash); err != nil {
		log.Info("Warning: failed to update metadata", "key", hashKey, "error", err)
		log.Info("Next export may require running 'bd import' first")
	}

	// Use RFC3339Nano for nanosecond precision to avoid race with file mtime (fixes #399)
	exportTime := time.Now().Format(time.RFC3339Nano)
	if err := store.SetMetadata(ctx, timeKey, exportTime); err != nil {
		log.Info("Warning: failed to update metadata", "key", timeKey, "error", err)
	}
	// Note: mtime tracking removed (git doesn't preserve mtime)
}

// validateDatabaseFingerprint checks that the database belongs to this repository
func validateDatabaseFingerprint(ctx context.Context, store storage.Storage, log *slog.Logger) error {

	// Get stored repo ID
	storedRepoID, err := store.GetMetadata(ctx, "repo_id")
	if err != nil && err.Error() != "metadata key not found: repo_id" {
		return fmt.Errorf("failed to read repo_id: %w", err)
	}

	// If no repo_id, this is a legacy database - require explicit migration
	if storedRepoID == "" {
		return fmt.Errorf(`
LEGACY DATABASE DETECTED!

This database was created before version 0.17.5 and lacks a repository fingerprint.
To continue using this database, you must explicitly set its repository ID:

  bd migrate --update-repo-id

This ensures the database is bound to this repository and prevents accidental
database sharing between different repositories.

If this is a fresh clone, run:
  rm -rf .beads && bd init

Note: Auto-claiming legacy databases is intentionally disabled to prevent
silent corruption when databases are copied between repositories.
`)
	}

	// Validate repo ID matches current repository
	currentRepoID, err := beads.ComputeRepoID()
	if err != nil {
		log.Info("Warning: could not compute current repository ID", "error", err)
		return nil
	}

	if storedRepoID != currentRepoID {
		return fmt.Errorf(`
DATABASE MISMATCH DETECTED!

This database belongs to a different repository:
  Database repo ID:  %s
  Current repo ID:   %s

This usually means:
  1. You copied a .beads directory from another repo (don't do this!)
  2. Git remote URL changed (run 'bd migrate --update-repo-id')
  3. Database corruption
  4. bd was upgraded and URL canonicalization changed

⚠️  CRITICAL: This mismatch can cause beads to incorrectly delete issues during sync!
   The git-history-backfill mechanism may treat your local issues as deleted
   because they don't exist in the remote repository's history.

Solutions:
  - If remote URL changed: bd migrate --update-repo-id
  - If bd was upgraded: bd migrate --update-repo-id
  - If wrong database: rm -rf .beads && bd init
  - If correct database: BEADS_IGNORE_REPO_MISMATCH=1 bd daemon
    (Warning: This can cause data corruption and unwanted deletions across clones!)
`, storedRepoID[:8], currentRepoID[:8])
	}

	log.Info("Repository fingerprint validated", "repo_id", currentRepoID[:8])
	return nil
}

// createExportFunc creates a function that only exports database to JSONL
// and optionally commits/pushes (no git pull or import). Used for mutation events.
func createExportFunc(ctx context.Context, store storage.Storage, autoCommit, autoPush bool, log *slog.Logger) func() {
	return performExport(ctx, store, autoCommit, autoPush, false, log)
}

// createLocalExportFunc creates a function that only exports database to JSONL
// without any git operations. Used for local-only mode with mutation events.
func createLocalExportFunc(ctx context.Context, store storage.Storage, log *slog.Logger) func() {
	return performExport(ctx, store, false, false, true, log)
}

// performExport is the shared implementation for export-only functions.
// skipGit: if true, skips all git operations (commits, pushes).
func performExport(ctx context.Context, store storage.Storage, autoCommit, autoPush, skipGit bool, log *slog.Logger) func() {
	return func() {
		exportCtx, exportCancel := context.WithTimeout(ctx, 30*time.Second)
		defer exportCancel()

		mode := "export"
		if skipGit {
			mode = "local export"
		}

		// Guard: Skip if sync-branch == current-branch (GH#1258)
		// Local-only mode (skipGit) doesn't use sync-branch, so skip the guard
		if !skipGit && shouldSkipDueToSameBranch(exportCtx, store, mode, log) {
			return
		}

		log.Info("Starting", "mode", mode)

		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			log.Info("Error: beads storage file not found")
			return
		}

		// Check for exclusive lock
		beadsDir := filepath.Dir(jsonlPath)
		skip, holder, err := types.ShouldSkipDatabase(beadsDir)
		if skip {
			if err != nil {
				log.Info("Skipping (lock check failed)", "mode", mode, "error", err)
			} else {
				log.Info("Skipping (locked)", "mode", mode, "holder", holder)
			}
			return
		}
		if holder != "" {
			log.Info("Removed stale lock, proceeding", "holder", holder)
		}

		// Pre-export validation
		if err := validatePreExport(exportCtx, store, jsonlPath); err != nil {
			log.Info("Pre-export validation failed", "error", err)
			return
		}

		// Export to JSONL
		if err := exportToJSONLWithStore(exportCtx, store, jsonlPath); err != nil {
			log.Info("Export failed", "error", err)
			return
		}
		log.Info("Exported to JSONL")

		// Export events to JSONL (non-fatal, opt-in via config)
		if config.GetBool("events-export") {
			eventsPath := filepath.Join(filepath.Dir(jsonlPath), "events.jsonl")
			if err := exportEventsToJSONL(exportCtx, store, eventsPath); err != nil {
				log.Info("Warning: events export failed", "error", err)
			}
		}

		// GH#885: Defer metadata updates until AFTER git commit succeeds.
		// This is a helper to finalize the export after git operations.
		finalizeExportMetadata := func() {
			// Update export metadata for multi-repo support with stable keys
			multiRepoPaths := getMultiRepoJSONLPaths()
			if multiRepoPaths != nil {
				// Multi-repo mode: update metadata for each JSONL with stable repo key
				for _, path := range multiRepoPaths {
					repoKey := getRepoKeyForPath(path)
					updateExportMetadata(exportCtx, store, path, log, repoKey)
				}
			} else {
				// Single-repo mode: update metadata for main JSONL
				updateExportMetadata(exportCtx, store, jsonlPath, log, "")
			}

			// Update database mtime to be >= JSONL mtime (fixes #278, #301, #321)
			// This prevents validatePreExport from incorrectly blocking on next export
			// with "JSONL is newer than database" after daemon auto-export
			// Dolt backend does not have a SQLite DB file; mtime touch is SQLite-only.
			// Use store.Path() to get the actual database location, not the JSONL directory,
			// since sync-branch exports write JSONL to a worktree but the DB stays in the main repo.
			if sqliteStore, ok := store.(*sqlite.SQLiteStorage); ok {
				dbPath := sqliteStore.Path()
				if err := TouchDatabaseFile(dbPath, jsonlPath); err != nil {
					log.Info("Warning: failed to update database mtime", "error", err)
				}
			}
		}

		// Auto-commit if enabled (skip in git-free mode)
		if autoCommit && !skipGit {
			// Try sync branch commit first
			// Use forceOverwrite=true because mutation-triggered exports (create, update, delete)
			// mean the local state is authoritative and should not be merged with worktree.
			// This is critical for delete mutations to be properly reflected in the sync branch.
			committed, err := syncBranchCommitAndPushWithOptions(exportCtx, store, autoPush, true, log)
			if err != nil {
				log.Info("Sync branch commit failed", "error", err)
				return
			}

			if committed {
				// GH#885: Finalize after sync branch commit succeeded
				finalizeExportMetadata()
			} else {
				// If sync branch not configured, use regular commit
				hasChanges, err := gitHasChanges(exportCtx, jsonlPath)
				if err != nil {
					log.Info("Error checking git status", "error", err)
					return
				}

				if hasChanges {
					message := fmt.Sprintf("bd daemon export: %s", time.Now().Format("2006-01-02 15:04:05"))
					if err := gitCommit(exportCtx, jsonlPath, message); err != nil {
						log.Info("Commit failed", "error", err)
						return
					}
					log.Info("Committed changes")

					// GH#885: Finalize after git commit succeeded, before push
					// Push failure shouldn't prevent metadata update since commit succeeded
					finalizeExportMetadata()

					// Auto-push if enabled (GH#872: use sync.remote config)
					if autoPush {
						configuredRemote, _ := store.GetConfig(exportCtx, "sync.remote")
						if err := gitPush(exportCtx, configuredRemote); err != nil {
							log.Info("Push failed", "error", err)
							return
						}
						log.Info("Pushed to remote")
					}
				} else {
					// No git changes but export happened - finalize metadata
					finalizeExportMetadata()
				}
			}
		} else if skipGit {
			// Git-free mode: finalize immediately since there's no git to wait for
			finalizeExportMetadata()
		}

		if skipGit {
			log.Info("Local export complete")
		} else {
			log.Info("Export complete")
		}
	}
}

// createAutoImportFunc creates a function that pulls from git and imports JSONL
// to database (no export). Used for file system change events.
func createAutoImportFunc(ctx context.Context, store storage.Storage, log *slog.Logger) func() {
	return performAutoImport(ctx, store, false, log)
}

// createLocalAutoImportFunc creates a function that imports from JSONL to database
// without any git operations. Used for local-only mode with file system change events.
func createLocalAutoImportFunc(ctx context.Context, store storage.Storage, log *slog.Logger) func() {
	return performAutoImport(ctx, store, true, log)
}

// performAutoImport is the shared implementation for import-only functions.
// skipGit: if true, skips git pull operations.
func performAutoImport(ctx context.Context, store storage.Storage, skipGit bool, log *slog.Logger) func() {
	return func() {
		importCtx, importCancel := context.WithTimeout(ctx, 1*time.Minute)
		defer importCancel()

		mode := "auto-import"
		if skipGit {
			mode = "local auto-import"
		}

		// Skip JSONL import in dolt-native mode (JSONL is export-only backup)
		if !ShouldImportJSONL(importCtx, store) {
			log.Info("Skipping (dolt-native mode)", "mode", mode)
			return
		}

		// Guard: Skip if sync-branch == current-branch (GH#1258)
		// Local-only mode (skipGit) doesn't use sync-branch, so skip the guard
		if !skipGit && shouldSkipDueToSameBranch(importCtx, store, mode, log) {
			return
		}

		// Check backoff before attempting sync (skip for local mode)
		if !skipGit {
			jsonlPath := findJSONLPath()
			if jsonlPath != "" {
				beadsDir := filepath.Dir(jsonlPath)
				if ShouldSkipSync(beadsDir) {
					log.Info("Skipping: in backoff period", "mode", mode)
					return
				}
			}
		}

		log.Info("Starting auto-import", "mode", mode)

		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			log.Info("Error: beads storage file not found")
			return
		}

		// Check for exclusive lock
		beadsDir := filepath.Dir(jsonlPath)
		skip, holder, err := types.ShouldSkipDatabase(beadsDir)
		if skip {
			if err != nil {
				log.Info("Skipping (lock check failed)", "mode", mode, "error", err)
			} else {
				log.Info("Skipping (locked)", "mode", mode, "holder", holder)
			}
			return
		}
		if holder != "" {
			log.Info("Removed stale lock, proceeding", "holder", holder)
		}

		// Check JSONL content hash to avoid redundant imports
		// Use content-based check (not mtime) to avoid git resurrection bug
		// Use getRepoKeyForPath for multi-repo support
		repoKey := getRepoKeyForPath(jsonlPath)
		if !hasJSONLChanged(importCtx, store, jsonlPath, repoKey) {
			log.Info("Skipping: JSONL content unchanged", "mode", mode)
			return
		}
		log.Info("JSONL content changed, proceeding", "mode", mode)

		// Pull from git if not in git-free mode
		if !skipGit {
			// SAFETY CHECK: Warn if there are uncommitted local changes
			// This helps detect race conditions where local work hasn't been pushed yet
			jsonlPath := findJSONLPath()
			if jsonlPath != "" {
				if hasLocalChanges, err := gitHasChanges(importCtx, jsonlPath); err == nil && hasLocalChanges {
					log.Info("WARNING: Uncommitted local changes detected", "path", jsonlPath)
					log.Info("   Pulling from remote may overwrite local unpushed changes.")
					log.Info("   Consider running 'bd sync' to commit and push your changes first.")
					// Continue anyway, but user has been warned
				}
			}

			// Try sync branch first
			pulled, err := syncBranchPull(importCtx, store, log)
			if err != nil {
				backoff := RecordSyncFailure(beadsDir, err.Error())
				log.Info("Sync branch pull failed", "error", err, "backoff", backoff)
				return
			}

			// If sync branch not configured, use regular pull (GH#872: use sync.remote config)
			if !pulled {
				configuredRemote, _ := store.GetConfig(importCtx, "sync.remote")
				if err := gitPull(importCtx, configuredRemote); err != nil {
					backoff := RecordSyncFailure(beadsDir, err.Error())
					log.Info("Pull failed", "error", err, "backoff", backoff)
					return
				}
				log.Info("Pulled from remote")
			}
		}

		// Count issues before import
		beforeCount, err := countDBIssues(importCtx, store)
		if err != nil {
			log.Info("Failed to count issues before import", "error", err)
			return
		}

		// Import from JSONL
		if err := importToJSONLWithStore(importCtx, store, jsonlPath); err != nil {
			log.Info("Import failed", "error", err)
			return
		}
		log.Info("Imported from JSONL")

		// Validate import
		afterCount, err := countDBIssues(importCtx, store)
		if err != nil {
			log.Info("Failed to count issues after import", "error", err)
			return
		}

		if err := validatePostImport(beforeCount, afterCount, jsonlPath); err != nil {
			log.Info("Post-import validation failed", "error", err)
			return
		}

		if skipGit {
			log.Info("Local auto-import complete")
		} else {
			// Record success to clear backoff state
			RecordSyncSuccess(beadsDir)
			log.Info("Auto-import complete")
		}
	}
}

// createSyncFunc creates a function that performs full sync cycle (export, commit, pull, import, push)
func createSyncFunc(ctx context.Context, store storage.Storage, autoCommit, autoPush bool, log *slog.Logger) func() {
	return performSync(ctx, store, autoCommit, autoPush, false, log)
}

// createLocalSyncFunc creates a function that performs local-only sync (export only, no git).
// Used when daemon is started with --local flag.
func createLocalSyncFunc(ctx context.Context, store storage.Storage, log *slog.Logger) func() {
	return performSync(ctx, store, false, false, true, log)
}

// performSync is the shared implementation for sync functions.
// skipGit: if true, skips all git operations (commits, pulls, pushes, snapshot capture, 3-way merge, import).
// Local-only mode only performs validation and export since there's no remote to sync with.
func performSync(ctx context.Context, store storage.Storage, autoCommit, autoPush, skipGit bool, log *slog.Logger) func() {
	return func() {
		syncCtx, syncCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer syncCancel()

		mode := "sync cycle"
		if skipGit {
			mode = "local sync cycle"
		}

		// Guard: Skip if sync-branch == current-branch (GH#1258)
		// Local-only mode (skipGit) doesn't use sync-branch, so skip the guard
		if !skipGit && shouldSkipDueToSameBranch(syncCtx, store, mode, log) {
			return
		}

		log.Info("Starting sync cycle", "mode", mode)

		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			log.Info("Error: beads storage file not found")
			return
		}

		// Cache multi-repo paths to avoid redundant calls
		multiRepoPaths := getMultiRepoJSONLPaths()

		// Check for exclusive lock before processing database
		beadsDir := filepath.Dir(jsonlPath)
		skip, holder, err := types.ShouldSkipDatabase(beadsDir)
		if skip {
			if err != nil {
				log.Info("Skipping database (lock check failed)", "error", err)
			} else {
				log.Info("Skipping database (locked)", "holder", holder)
			}
			return
		}
		if holder != "" {
			log.Info("Removed stale lock, proceeding", "holder", holder, "mode", mode)
		}

		// Integrity check: validate before export
		if err := validatePreExport(syncCtx, store, jsonlPath); err != nil {
			log.Info("Pre-export validation failed", "error", err)
			return
		}

		// Check for duplicate IDs (database corruption)
		if err := checkDuplicateIDs(syncCtx, store); err != nil {
			log.Info("Duplicate ID check failed", "error", err)
			return
		}

		// Check for orphaned dependencies (warns but doesn't fail)
		if orphaned, err := checkOrphanedDeps(syncCtx, store); err != nil {
			log.Info("Orphaned dependency check failed", "error", err)
		} else if len(orphaned) > 0 {
			log.Info("Found orphaned dependencies", "count", len(orphaned), "orphaned", orphaned)
		}

		if err := exportToJSONLWithStore(syncCtx, store, jsonlPath); err != nil {
			log.Info("Export failed", "error", err)
			return
		}
		log.Info("Exported to JSONL")

		// Export events to JSONL (non-fatal, opt-in via config)
		if config.GetBool("events-export") {
			syncEventsPath := filepath.Join(beadsDir, "events.jsonl")
			if err := exportEventsToJSONL(syncCtx, store, syncEventsPath); err != nil {
				log.Info("Warning: events export failed", "error", err)
			}
		}

		// GH#885: Defer metadata updates until AFTER git commit succeeds.
		// Define helper to finalize after git operations.
		finalizeExportMetadata := func() {
			// Update export metadata for multi-repo support with stable keys
			if multiRepoPaths != nil {
				// Multi-repo mode: update metadata for each JSONL with stable repo key
				for _, path := range multiRepoPaths {
					repoKey := getRepoKeyForPath(path)
					updateExportMetadata(syncCtx, store, path, log, repoKey)
				}
			} else {
				// Single-repo mode: update metadata for main JSONL
				updateExportMetadata(syncCtx, store, jsonlPath, log, "")
			}

			// Update database mtime to be >= JSONL mtime
			// This prevents validatePreExport from incorrectly blocking on next export
			// Dolt backend does not have a SQLite DB file; mtime touch is SQLite-only.
			// Use store.Path() to get the actual database location, not the JSONL directory,
			// since sync-branch exports write JSONL to a worktree but the DB stays in the main repo.
			if sqliteStore, ok := store.(*sqlite.SQLiteStorage); ok {
				dbPath := sqliteStore.Path()
				if err := TouchDatabaseFile(dbPath, jsonlPath); err != nil {
					log.Info("Warning: failed to update database mtime", "error", err)
				}
			}
		}

		// Skip git operations, snapshot capture, deletion tracking, and import in local-only mode
		// Local-only sync is export-only since there's no remote to sync with
		if skipGit {
			// Git-free mode: finalize immediately since there's no git to wait for
			finalizeExportMetadata()
			log.Info("Local sync complete", "mode", mode)
			return
		}

		// In dolt-native mode, JSONL is export-only backup — skip git sync and import
		if !ShouldImportJSONL(syncCtx, store) {
			finalizeExportMetadata()
			log.Info("Sync complete (dolt-native mode, export-only)", "mode", mode)
			return
		}

		// ---- Git operations start here ----

		// Capture left snapshot (pre-pull state) for 3-way merge
		// This is mandatory for deletion tracking integrity
		// In multi-repo mode, capture snapshots for all JSONL files
		if multiRepoPaths != nil {
			// Multi-repo mode: snapshot each JSONL file
			for _, path := range multiRepoPaths {
				if err := captureLeftSnapshot(path); err != nil {
					log.Info("Error: failed to capture snapshot", "path", path, "error", err)
					return
				}
			}
			log.Info("Captured snapshots (multi-repo mode)", "count", len(multiRepoPaths))
		} else {
			// Single-repo mode: snapshot the main JSONL
			if err := captureLeftSnapshot(jsonlPath); err != nil {
				log.Info("Error: failed to capture snapshot", "error", err)
				return
			}
		}

		if autoCommit {
			// Try sync branch commit first
			committed, err := syncBranchCommitAndPush(syncCtx, store, autoPush, log)
			if err != nil {
				log.Info("Sync branch commit failed", "error", err)
				return
			}

			// If sync branch not configured, use regular commit
			if !committed {
				hasChanges, err := gitHasChanges(syncCtx, jsonlPath)
				if err != nil {
					log.Info("Error checking git status", "error", err)
					return
				}

				if hasChanges {
					message := fmt.Sprintf("bd daemon sync: %s", time.Now().Format("2006-01-02 15:04:05"))
					if err := gitCommit(syncCtx, jsonlPath, message); err != nil {
						log.Info("Commit failed", "error", err)
						return
					}
					log.Info("Committed changes")
				}
			}

			// GH#885: NOW finalize metadata after git commit succeeded
			finalizeExportMetadata()
		}

		// Pull (try sync branch first)
		pulled, err := syncBranchPull(syncCtx, store, log)
		if err != nil {
			log.Info("Sync branch pull failed", "error", err)
			return
		}

		// If sync branch not configured, use regular pull (GH#872: use sync.remote config)
		if !pulled {
			configuredRemote, _ := store.GetConfig(syncCtx, "sync.remote")
			if err := gitPull(syncCtx, configuredRemote); err != nil {
				log.Info("Pull failed", "error", err)
				return
			}
			log.Info("Pulled from remote")
		}

		// Count issues before import for validation
		beforeCount, err := countDBIssues(syncCtx, store)
		if err != nil {
			log.Info("Failed to count issues before import", "error", err)
			return
		}

		// Perform 3-way merge and prune deletions
		// In multi-repo mode, apply deletions for each JSONL file
		if multiRepoPaths != nil {
			// Multi-repo mode: merge/prune for each JSONL
			for _, path := range multiRepoPaths {
				if err := applyDeletionsFromMerge(syncCtx, store, path); err != nil {
					log.Info("Error during 3-way merge", "path", path, "error", err)
					return
				}
			}
			log.Info("Applied deletions", "repo_count", len(multiRepoPaths))
		} else {
			// Single-repo mode
			if err := applyDeletionsFromMerge(syncCtx, store, jsonlPath); err != nil {
				log.Info("Error during 3-way merge", "error", err)
				return
			}
		}

		if err := importToJSONLWithStore(syncCtx, store, jsonlPath); err != nil {
			log.Info("Import failed", "error", err)
			return
		}
		log.Info("Imported from JSONL")

		// Update database mtime after import (fixes #278, #301, #321)
		// Sync branch import can update JSONL timestamp, so ensure DB >= JSONL
		// Dolt backend does not have a SQLite DB file; mtime touch is SQLite-only.
		// Use store.Path() to get the actual database location, not the JSONL directory,
		// since sync-branch imports read JSONL from a worktree but the DB stays in the main repo.
		if sqliteStore, ok := store.(*sqlite.SQLiteStorage); ok {
			dbPath := sqliteStore.Path()
			if err := TouchDatabaseFile(dbPath, jsonlPath); err != nil {
				log.Info("Warning: failed to update database mtime", "error", err)
			}
		}

		// Validate import didn't cause data loss
		afterCount, err := countDBIssues(syncCtx, store)
		if err != nil {
			log.Info("Failed to count issues after import", "error", err)
			return
		}

		if err := validatePostImport(beforeCount, afterCount, jsonlPath); err != nil {
			log.Info("Post-import validation failed", "error", err)
			return
		}

		// Update base snapshot after successful import
		// In multi-repo mode, update snapshots for all JSONL files
		if multiRepoPaths != nil {
			for _, path := range multiRepoPaths {
				if err := updateBaseSnapshot(path); err != nil {
					log.Info("Warning: failed to update base snapshot", "path", path, "error", err)
				}
			}
		} else {
			if err := updateBaseSnapshot(jsonlPath); err != nil {
				log.Info("Warning: failed to update base snapshot", "error", err)
			}
		}

		// Clean up temporary snapshot files after successful merge
		// In multi-repo mode, clean up snapshots for all JSONL files
		if multiRepoPaths != nil {
			for _, path := range multiRepoPaths {
				sm := NewSnapshotManager(path)
				if err := sm.Cleanup(); err != nil {
					log.Info("Warning: failed to clean up snapshots", "path", path, "error", err)
				}
			}
		} else {
			sm := NewSnapshotManager(jsonlPath)
			if err := sm.Cleanup(); err != nil {
				log.Info("Warning: failed to clean up snapshots", "error", err)
			}
		}

		// GH#872: use sync.remote config
		if autoPush && autoCommit {
			configuredRemote, _ := store.GetConfig(syncCtx, "sync.remote")
			if err := gitPush(syncCtx, configuredRemote); err != nil {
				log.Info("Push failed", "error", err)
				return
			}
			log.Info("Pushed to remote")
		}

		log.Info("Sync cycle complete")
	}
}
