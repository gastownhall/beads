package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// exportToJSONLWithStore exports issues to JSONL using the provided store.
// If multi-repo mode is configured, routes issues to their respective JSONL files.
// Otherwise, exports to a single JSONL file.
func exportToJSONLWithStore(ctx context.Context, store storage.Storage, jsonlPath string) error {
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
	// Get all issues
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("failed to get issues: %w", err)
	}

	// Safety check: prevent exporting empty database over non-empty JSONL
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
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].ID < issues[j].ID
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

// validateDatabaseFingerprint checks that the database belongs to this repository
func validateDatabaseFingerprint(store storage.Storage, log *daemonLogger) error {
	ctx := context.Background()

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
		log.log("Warning: could not compute current repository ID: %v", err)
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

Solutions:
  - If remote URL changed: bd migrate --update-repo-id
  - If bd was upgraded: bd migrate --update-repo-id
  - If wrong database: rm -rf .beads && bd init
  - If correct database: BEADS_IGNORE_REPO_MISMATCH=1 bd daemon
    (Warning: This can cause data corruption across clones!)
`, storedRepoID[:8], currentRepoID[:8])
	}

	log.log("Repository fingerprint validated: %s", currentRepoID[:8])
	return nil
}

// createExportFunc creates a function that only exports database to JSONL
// and optionally commits/pushes (no git pull or import). Used for mutation events.
func createExportFunc(ctx context.Context, store storage.Storage, autoCommit, autoPush bool, log daemonLogger) func() {
	return func() {
		exportCtx, exportCancel := context.WithTimeout(ctx, 30*time.Second)
		defer exportCancel()

		log.log("Starting export...")

		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			log.log("Error: JSONL path not found")
			return
		}

		// Check for exclusive lock
		beadsDir := filepath.Dir(jsonlPath)
		skip, holder, err := types.ShouldSkipDatabase(beadsDir)
		if skip {
			if err != nil {
				log.log("Skipping export (lock check failed: %v)", err)
			} else {
				log.log("Skipping export (locked by %s)", holder)
			}
			return
		}
		if holder != "" {
			log.log("Removed stale lock (%s), proceeding", holder)
		}

		// Pre-export validation
		if err := validatePreExport(exportCtx, store, jsonlPath); err != nil {
			log.log("Pre-export validation failed: %v", err)
			return
		}

		// Export to JSONL
		if err := exportToJSONLWithStore(exportCtx, store, jsonlPath); err != nil {
			log.log("Export failed: %v", err)
			return
		}
		log.log("Exported to JSONL")

		// Auto-commit if enabled
		if autoCommit {
			// Try sync branch commit first
			committed, err := syncBranchCommitAndPush(exportCtx, store, autoPush, log)
			if err != nil {
				log.log("Sync branch commit failed: %v", err)
				return
			}

			// If sync branch not configured, use regular commit
			if !committed {
				hasChanges, err := gitHasChanges(exportCtx, jsonlPath)
				if err != nil {
					log.log("Error checking git status: %v", err)
					return
				}

				if hasChanges {
					message := fmt.Sprintf("bd daemon export: %s", time.Now().Format("2006-01-02 15:04:05"))
					if err := gitCommit(exportCtx, jsonlPath, message); err != nil {
						log.log("Commit failed: %v", err)
						return
					}
					log.log("Committed changes")

					// Auto-push if enabled
					if autoPush {
						if err := gitPush(exportCtx); err != nil {
							log.log("Push failed: %v", err)
							return
						}
						log.log("Pushed to remote")
					}
				}
			}
		}

		log.log("Export complete")
	}
}

// createAutoImportFunc creates a function that pulls from git and imports JSONL
// to database (no export). Used for file system change events.
func createAutoImportFunc(ctx context.Context, store storage.Storage, log daemonLogger) func() {
	return func() {
		importCtx, importCancel := context.WithTimeout(ctx, 1*time.Minute)
		defer importCancel()

		log.log("Starting auto-import...")

		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			log.log("Error: JSONL path not found")
			return
		}

		// Check for exclusive lock
		beadsDir := filepath.Dir(jsonlPath)
		skip, holder, err := types.ShouldSkipDatabase(beadsDir)
		if skip {
			if err != nil {
				log.log("Skipping import (lock check failed: %v)", err)
			} else {
				log.log("Skipping import (locked by %s)", holder)
			}
			return
		}
		if holder != "" {
			log.log("Removed stale lock (%s), proceeding", holder)
		}

		// Check JSONL modification time to avoid redundant imports
		jsonlInfo, err := os.Stat(jsonlPath)
		if err != nil {
			log.log("Failed to stat JSONL: %v", err)
			return
		}

		// Get database modification time
		dbPath := filepath.Join(beadsDir, "beads.db")
		dbInfo, err := os.Stat(dbPath)
		if err != nil {
			log.log("Failed to stat database: %v", err)
			return
		}

		// Skip if JSONL is older than database (nothing new to import)
		if !jsonlInfo.ModTime().After(dbInfo.ModTime()) {
			log.log("Skipping import: JSONL not newer than database")
			return
		}

		// Pull from git (try sync branch first)
		pulled, err := syncBranchPull(importCtx, store, log)
		if err != nil {
			log.log("Sync branch pull failed: %v", err)
			return
		}

		// If sync branch not configured, use regular pull
		if !pulled {
			if err := gitPull(importCtx); err != nil {
				log.log("Pull failed: %v", err)
				return
			}
			log.log("Pulled from remote")
		}

		// Count issues before import
		beforeCount, err := countDBIssues(importCtx, store)
		if err != nil {
			log.log("Failed to count issues before import: %v", err)
			return
		}

		// Import from JSONL
		if err := importToJSONLWithStore(importCtx, store, jsonlPath); err != nil {
			log.log("Import failed: %v", err)
			return
		}
		log.log("Imported from JSONL")

		// Validate import
		afterCount, err := countDBIssues(importCtx, store)
		if err != nil {
			log.log("Failed to count issues after import: %v", err)
			return
		}

		if err := validatePostImport(beforeCount, afterCount); err != nil {
			log.log("Post-import validation failed: %v", err)
			return
		}

		log.log("Auto-import complete")
	}
}

// createSyncFunc creates a function that performs full sync cycle (export, commit, pull, import, push)
func createSyncFunc(ctx context.Context, store storage.Storage, autoCommit, autoPush bool, log daemonLogger) func() {
	return func() {
		syncCtx, syncCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer syncCancel()

		log.log("Starting sync cycle...")

		jsonlPath := findJSONLPath()
		if jsonlPath == "" {
			log.log("Error: JSONL path not found")
			return
		}

		// Cache multi-repo paths to avoid redundant calls (bd-we4p)
		multiRepoPaths := getMultiRepoJSONLPaths()

		// Check for exclusive lock before processing database
		beadsDir := filepath.Dir(jsonlPath)
		skip, holder, err := types.ShouldSkipDatabase(beadsDir)
		if skip {
			if err != nil {
				log.log("Skipping database (lock check failed: %v)", err)
			} else {
				log.log("Skipping database (locked by %s)", holder)
			}
			return
		}
		if holder != "" {
			log.log("Removed stale lock (%s), proceeding with sync", holder)
		}

		// Integrity check: validate before export
		if err := validatePreExport(syncCtx, store, jsonlPath); err != nil {
			log.log("Pre-export validation failed: %v", err)
			return
		}

		// Check for duplicate IDs (database corruption)
		if err := checkDuplicateIDs(syncCtx, store); err != nil {
			log.log("Duplicate ID check failed: %v", err)
			return
		}

		// Check for orphaned dependencies (warns but doesn't fail)
		if orphaned, err := checkOrphanedDeps(syncCtx, store); err != nil {
			log.log("Orphaned dependency check failed: %v", err)
		} else if len(orphaned) > 0 {
			log.log("Found %d orphaned dependencies: %v", len(orphaned), orphaned)
		}

		if err := exportToJSONLWithStore(syncCtx, store, jsonlPath); err != nil {
			log.log("Export failed: %v", err)
			return
		}
		log.log("Exported to JSONL")

		// Capture left snapshot (pre-pull state) for 3-way merge
		// This is mandatory for deletion tracking integrity
		// In multi-repo mode, capture snapshots for all JSONL files
		if multiRepoPaths != nil {
			// Multi-repo mode: snapshot each JSONL file
			for _, path := range multiRepoPaths {
				if err := captureLeftSnapshot(path); err != nil {
					log.log("Error: failed to capture snapshot for %s: %v", path, err)
					return
				}
			}
			log.log("Captured %d snapshots (multi-repo mode)", len(multiRepoPaths))
		} else {
			// Single-repo mode: snapshot the main JSONL
			if err := captureLeftSnapshot(jsonlPath); err != nil {
				log.log("Error: failed to capture snapshot (required for deletion tracking): %v", err)
				return
			}
		}

		if autoCommit {
			// Try sync branch commit first
			committed, err := syncBranchCommitAndPush(syncCtx, store, autoPush, log)
			if err != nil {
				log.log("Sync branch commit failed: %v", err)
				return
			}

			// If sync branch not configured, use regular commit
			if !committed {
				hasChanges, err := gitHasChanges(syncCtx, jsonlPath)
				if err != nil {
					log.log("Error checking git status: %v", err)
					return
				}

				if hasChanges {
					message := fmt.Sprintf("bd daemon sync: %s", time.Now().Format("2006-01-02 15:04:05"))
					if err := gitCommit(syncCtx, jsonlPath, message); err != nil {
						log.log("Commit failed: %v", err)
						return
					}
					log.log("Committed changes")
				}
			}
		}

		// Pull (try sync branch first)
		pulled, err := syncBranchPull(syncCtx, store, log)
		if err != nil {
			log.log("Sync branch pull failed: %v", err)
			return
		}

		// If sync branch not configured, use regular pull
		if !pulled {
			if err := gitPull(syncCtx); err != nil {
				log.log("Pull failed: %v", err)
				return
			}
			log.log("Pulled from remote")
		}

		// Count issues before import for validation
		beforeCount, err := countDBIssues(syncCtx, store)
		if err != nil {
			log.log("Failed to count issues before import: %v", err)
			return
		}

		// Perform 3-way merge and prune deletions
		// In multi-repo mode, apply deletions for each JSONL file
		if multiRepoPaths != nil {
		 // Multi-repo mode: merge/prune for each JSONL
		for _, path := range multiRepoPaths {
				if err := applyDeletionsFromMerge(syncCtx, store, path); err != nil {
					log.log("Error during 3-way merge for %s: %v", path, err)
					return
				}
			}
			log.log("Applied deletions from %d repos", len(multiRepoPaths))
		} else {
			// Single-repo mode
			if err := applyDeletionsFromMerge(syncCtx, store, jsonlPath); err != nil {
				log.log("Error during 3-way merge: %v", err)
				return
			}
		}

		if err := importToJSONLWithStore(syncCtx, store, jsonlPath); err != nil {
			log.log("Import failed: %v", err)
			return
		}
		log.log("Imported from JSONL")

		// Validate import didn't cause data loss
		afterCount, err := countDBIssues(syncCtx, store)
		if err != nil {
			log.log("Failed to count issues after import: %v", err)
			return
		}

		if err := validatePostImport(beforeCount, afterCount); err != nil {
			log.log("Post-import validation failed: %v", err)
			return
		}

		// Update base snapshot after successful import
		// In multi-repo mode, update snapshots for all JSONL files
		if multiRepoPaths != nil {
		 for _, path := range multiRepoPaths {
				if err := updateBaseSnapshot(path); err != nil {
					log.log("Warning: failed to update base snapshot for %s: %v", path, err)
				}
			}
		} else {
			if err := updateBaseSnapshot(jsonlPath); err != nil {
				log.log("Warning: failed to update base snapshot: %v", err)
			}
		}

		if autoPush && autoCommit {
			if err := gitPush(syncCtx); err != nil {
				log.log("Push failed: %v", err)
				return
			}
			log.log("Pushed to remote")
		}

		log.log("Sync cycle complete")
	}
}
