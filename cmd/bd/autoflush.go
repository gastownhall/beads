package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fatih/color"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/types"
	"golang.org/x/mod/semver"
)

// outputJSON outputs data as pretty-printed JSON
func outputJSON(v interface{}) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

// findJSONLPath finds the JSONL file path for the current database
// findJSONLPath discovers the JSONL file path for the current database and ensures
// the parent directory exists. Uses beads.FindJSONLPath() for discovery (checking
// BEADS_JSONL env var first, then using .beads/issues.jsonl next to the database).
//
// Creates the .beads directory if it doesn't exist (important for new databases).
// If directory creation fails, returns the path anyway - the subsequent write will
// fail with a clearer error message.
//
// Thread-safe: No shared state access.
func findJSONLPath() string {
	// Use public API for path discovery
	jsonlPath := beads.FindJSONLPath(dbPath)

	// Ensure the directory exists (important for new databases)
	// This is the only difference from the public API - we create the directory
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0750); err != nil {
		// If we can't create the directory, return discovered path anyway
		// (the subsequent write will fail with a clearer error)
		return jsonlPath
	}

	return jsonlPath
}

// autoImportIfNewer checks if JSONL content changed (via hash) and imports if so
// Fixes bd-84: Hash-based comparison is git-proof (mtime comparison fails after git pull)
// Fixes bd-228: Now uses collision detection to prevent silently overwriting local changes
func autoImportIfNewer() {
	// Find JSONL path
	jsonlPath := findJSONLPath()

	// Read JSONL file
	jsonlData, err := os.ReadFile(jsonlPath)
	if err != nil {
		// JSONL doesn't exist or can't be accessed, skip import
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: auto-import skipped, JSONL not found: %v\n", err)
		}
		return
	}

	// Compute current JSONL hash
	hasher := sha256.New()
	hasher.Write(jsonlData)
	currentHash := hex.EncodeToString(hasher.Sum(nil))

	// Get last import hash from DB metadata
	ctx := context.Background()
	lastHash, err := store.GetMetadata(ctx, "last_import_hash")
	if err != nil {
		// Metadata error - treat as first import rather than skipping (bd-663)
		// This allows auto-import to recover from corrupt/missing metadata
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: metadata read failed (%v), treating as first import\n", err)
		}
		lastHash = ""
	}

	// Compare hashes
	if currentHash == lastHash {
		// Content unchanged, skip import
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: auto-import skipped, JSONL unchanged (hash match)\n")
		}
		return
	}

	if os.Getenv("BD_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "Debug: auto-import triggered (hash changed)\n")
	}

	// Check for Git merge conflict markers (bd-270)
	// Only match if they appear as standalone lines (not embedded in JSON strings)
	lines := bytes.Split(jsonlData, []byte("\n"))
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("<<<<<<< ")) ||
			bytes.Equal(trimmed, []byte("=======")) ||
			bytes.HasPrefix(trimmed, []byte(">>>>>>> ")) {
			fmt.Fprintf(os.Stderr, "\n❌ Git merge conflict detected in %s\n\n", jsonlPath)
			fmt.Fprintf(os.Stderr, "The JSONL file contains unresolved merge conflict markers.\n")
			fmt.Fprintf(os.Stderr, "This prevents auto-import from loading your issues.\n\n")
			fmt.Fprintf(os.Stderr, "To resolve:\n")
			fmt.Fprintf(os.Stderr, "  1. Resolve the merge conflict in your Git client, OR\n")
			fmt.Fprintf(os.Stderr, "  2. Export from database to regenerate clean JSONL:\n")
			fmt.Fprintf(os.Stderr, "     bd export -o %s\n\n", jsonlPath)
			fmt.Fprintf(os.Stderr, "After resolving, commit the fixed JSONL file.\n")
			return
		}
	}

	// Content changed - parse all issues
	scanner := bufio.NewScanner(bytes.NewReader(jsonlData))
	scanner.Buffer(make([]byte, 0, 1024), 2*1024*1024) // 2MB buffer for large JSON lines
	var allIssues []*types.Issue
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			// Parse error, skip this import
			snippet := line
			if len(snippet) > 80 {
				snippet = snippet[:80] + "..."
			}
			fmt.Fprintf(os.Stderr, "Auto-import skipped: parse error at line %d: %v\nSnippet: %s\n", lineNo, err, snippet)
			return
		}

		// Fix closed_at invariant: closed issues must have closed_at timestamp
		if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
			now := time.Now()
			issue.ClosedAt = &now
		}

		allIssues = append(allIssues, &issue)
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Auto-import skipped: scanner error: %v\n", err)
		return
	}

	// Clear export_hashes before import to prevent staleness (bd-160)
	// Import operations may add/update issues, so export_hashes entries become invalid
	if err := store.ClearAllExportHashes(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to clear export_hashes before import: %v\n", err)
	}
	
	// Use shared import logic (bd-157)
	opts := ImportOptions{

		DryRun:               false,
		SkipUpdate:           false,
		Strict:               false,
		SkipPrefixValidation: true, // Auto-import is lenient about prefixes
	}

	result, err := importIssuesCore(ctx, dbPath, store, allIssues, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Auto-import failed: %v\n", err)
		return
	}

	// Show collision remapping notification if any occurred
	if len(result.IDMapping) > 0 {
		// Build title lookup map to avoid O(n^2) search
		titleByID := make(map[string]string)
		for _, issue := range allIssues {
			titleByID[issue.ID] = issue.Title
		}

		// Sort remappings by old ID for consistent output
		type mapping struct {
			oldID string
			newID string
		}
		mappings := make([]mapping, 0, len(result.IDMapping))
		for oldID, newID := range result.IDMapping {
			mappings = append(mappings, mapping{oldID, newID})
		}
		sort.Slice(mappings, func(i, j int) bool {
			return mappings[i].oldID < mappings[j].oldID
		})

		maxShow := 10
		numRemapped := len(mappings)
		if numRemapped < maxShow {
			maxShow = numRemapped
		}

		fmt.Fprintf(os.Stderr, "\nAuto-import: remapped %d colliding issue(s) to new IDs:\n", numRemapped)
		for i := 0; i < maxShow; i++ {
			m := mappings[i]
			title := titleByID[m.oldID]
			fmt.Fprintf(os.Stderr, "  %s → %s (%s)\n", m.oldID, m.newID, title)
		}
		if numRemapped > maxShow {
			fmt.Fprintf(os.Stderr, "  ... and %d more\n", numRemapped-maxShow)
		}
		fmt.Fprintf(os.Stderr, "\n")
	}

	// Schedule export to sync JSONL after successful import
	changed := (result.Created + result.Updated + len(result.IDMapping)) > 0
	if changed {
		if len(result.IDMapping) > 0 {
			// Remappings may affect many issues, do a full export
			markDirtyAndScheduleFullExport()
		} else {
			// Regular import, incremental export is fine
			markDirtyAndScheduleFlush()
		}
	}

	// Store new hash after successful import
	if err := store.SetMetadata(ctx, "last_import_hash", currentHash); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update last_import_hash after import: %v\n", err)
		fmt.Fprintf(os.Stderr, "This may cause auto-import to retry the same import on next operation.\n")
	}

	// Store import timestamp (bd-159: for staleness detection)
	importTime := time.Now().Format(time.RFC3339)
	if err := store.SetMetadata(ctx, "last_import_time", importTime); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update last_import_time after import: %v\n", err)
	}
}

// checkVersionMismatch checks if the binary version matches the database version
// and warns the user if they're running an outdated binary
func checkVersionMismatch() {
	ctx := context.Background()

	// Get the database version (version that last wrote to this DB)
	dbVersion, err := store.GetMetadata(ctx, "bd_version")
	if err != nil {
		// Metadata error - skip check (shouldn't happen, but be defensive)
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: version check skipped, metadata error: %v\n", err)
		}
		return
	}

	// If no version stored, this is an old database - store current version and continue
	if dbVersion == "" {
		_ = store.SetMetadata(ctx, "bd_version", Version)
		return
	}

	// Compare versions: warn if binary is older than database
	if dbVersion != Version {
		yellow := color.New(color.FgYellow, color.Bold).SprintFunc()
		fmt.Fprintf(os.Stderr, "\n%s\n", yellow("⚠️  WARNING: Version mismatch detected!"))
		fmt.Fprintf(os.Stderr, "%s\n", yellow(fmt.Sprintf("⚠️  Your bd binary (v%s) differs from the database version (v%s)", Version, dbVersion)))

		// Use semantic version comparison (requires v prefix)
		binaryVer := "v" + Version
		dbVer := "v" + dbVersion

		// semver.Compare returns -1 if binaryVer < dbVer, 0 if equal, 1 if binaryVer > dbVer
		cmp := semver.Compare(binaryVer, dbVer)

		if cmp < 0 {
			// Binary is older than database
			fmt.Fprintf(os.Stderr, "%s\n", yellow("⚠️  Your binary appears to be OUTDATED."))
			fmt.Fprintf(os.Stderr, "%s\n\n", yellow("⚠️  Some features may not work correctly. Rebuild: go build -o bd ./cmd/bd"))
		} else if cmp > 0 {
			// Binary is newer than database
			fmt.Fprintf(os.Stderr, "%s\n", yellow("⚠️  Your binary appears NEWER than the database."))
			fmt.Fprintf(os.Stderr, "%s\n\n", yellow("⚠️  The database will be upgraded automatically."))
			// Update stored version to current
			_ = store.SetMetadata(ctx, "bd_version", Version)
		}
	}

	// Always update the version metadata to track last-used version
	// This is safe even if versions match (idempotent operation)
	_ = store.SetMetadata(ctx, "bd_version", Version)
}

// markDirtyAndScheduleFlush marks the database as dirty and schedules a flush
// markDirtyAndScheduleFlush marks the database as dirty and schedules a debounced
// export to JSONL. Uses a timer that resets on each call - flush occurs 5 seconds
// after the LAST database modification (not the first).
//
// Debouncing behavior: If multiple operations happen within 5 seconds, the timer
// resets each time, and only one flush occurs after the burst of activity completes.
// This prevents excessive writes during rapid issue creation/updates.
//
// Flush-on-exit guarantee: PersistentPostRun cancels the timer and flushes immediately
// before the command exits, ensuring no data is lost even if the timer hasn't fired.
//
// Thread-safe: Protected by flushMutex. Safe to call from multiple goroutines.
// No-op if auto-flush is disabled via --no-auto-flush flag.
func markDirtyAndScheduleFlush() {
	if !autoFlushEnabled {
		return
	}

	flushMutex.Lock()
	defer flushMutex.Unlock()

	isDirty = true

	// Cancel existing timer if any
	if flushTimer != nil {
		flushTimer.Stop()
		flushTimer = nil
	}

	// Schedule new flush
	flushTimer = time.AfterFunc(getDebounceDuration(), func() {
		flushToJSONL()
	})
}

// markDirtyAndScheduleFullExport marks DB as needing a full export (for ID-changing operations)
func markDirtyAndScheduleFullExport() {
	if !autoFlushEnabled {
		return
	}

	flushMutex.Lock()
	defer flushMutex.Unlock()

	isDirty = true
	needsFullExport = true // Force full export, not incremental

	// Cancel existing timer if any
	if flushTimer != nil {
		flushTimer.Stop()
		flushTimer = nil
	}

	// Schedule new flush
	flushTimer = time.AfterFunc(getDebounceDuration(), func() {
		flushToJSONL()
	})
}

// clearAutoFlushState cancels pending flush and marks DB as clean (after manual export)
func clearAutoFlushState() {
	flushMutex.Lock()
	defer flushMutex.Unlock()

	// Cancel pending timer
	if flushTimer != nil {
		flushTimer.Stop()
		flushTimer = nil
	}

	// Clear dirty flag
	isDirty = false

	// Reset failure counter (manual export succeeded)
	flushFailureCount = 0
	lastFlushError = nil
}

// writeJSONLAtomic writes issues to a JSONL file atomically using temp file + rename.
// This is the common implementation used by both flushToJSONL (SQLite mode) and
// writeIssuesToJSONL (--no-db mode).
//
// Atomic write pattern:
//
//	1. Create temp file with PID suffix: issues.jsonl.tmp.12345
//	2. Write all issues as JSONL to temp file
//	3. Close temp file
//	4. Atomic rename: temp → target
//	5. Set file permissions to 0644
//
// Error handling: Returns error on any failure. Cleanup is guaranteed via defer.
// Thread-safe: No shared state access. Safe to call from multiple goroutines.
// validateJSONLIntegrity checks if JSONL file hash matches stored hash.
// If mismatch detected, clears export_hashes and logs warning (bd-160).
// Returns (needsFullExport, error) where needsFullExport=true if export_hashes was cleared.
func validateJSONLIntegrity(ctx context.Context, jsonlPath string) (bool, error) {
	// Get stored JSONL file hash
	storedHash, err := store.GetJSONLFileHash(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get stored JSONL hash: %w", err)
	}
	
	// If no hash stored, this is first export - skip validation
	if storedHash == "" {
		return false, nil
	}
	
	// Read current JSONL file
	jsonlData, err := os.ReadFile(jsonlPath)
	if err != nil {
		if os.IsNotExist(err) {
			// JSONL doesn't exist but we have a stored hash - clear export_hashes
			fmt.Fprintf(os.Stderr, "⚠️  WARNING: JSONL file missing but export_hashes exist. Clearing export_hashes.\n")
			if err := store.ClearAllExportHashes(ctx); err != nil {
				return false, fmt.Errorf("failed to clear export_hashes: %w", err)
			}
			return true, nil // Signal full export needed
		}
		return false, fmt.Errorf("failed to read JSONL file: %w", err)
	}
	
	// Compute current JSONL hash
	hasher := sha256.New()
	hasher.Write(jsonlData)
	currentHash := hex.EncodeToString(hasher.Sum(nil))
	
	// Compare hashes
	if currentHash != storedHash {
		fmt.Fprintf(os.Stderr, "⚠️  WARNING: JSONL file hash mismatch detected (bd-160)\n")
		fmt.Fprintf(os.Stderr, "  This indicates JSONL and export_hashes are out of sync.\n")
		fmt.Fprintf(os.Stderr, "  Clearing export_hashes to force full re-export.\n")
		
		// Clear export_hashes to force full re-export
		if err := store.ClearAllExportHashes(ctx); err != nil {
			return false, fmt.Errorf("failed to clear export_hashes: %w", err)
		}
		return true, nil // Signal full export needed
	}
	
	return false, nil
}

func writeJSONLAtomic(jsonlPath string, issues []*types.Issue) ([]string, error) {
	// Sort issues by ID for consistent output
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].ID < issues[j].ID
	})

	// Create temp file with PID suffix to avoid collisions (bd-306)
	tempPath := fmt.Sprintf("%s.tmp.%d", jsonlPath, os.Getpid())
	f, err := os.Create(tempPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}

	// Ensure cleanup on failure
	defer func() {
		if f != nil {
			_ = f.Close()
			_ = os.Remove(tempPath)
		}
	}()

	// Write all issues as JSONL (timestamp-only deduplication DISABLED - bd-160)
	encoder := json.NewEncoder(f)
	skippedCount := 0
	exportedIDs := make([]string, 0, len(issues))
	
	for _, issue := range issues {
		// DISABLED: timestamp-only deduplication causes data loss (bd-160)
		// skip, err := shouldSkipExport(ctx, issue)
		// if err != nil {
		// 	if os.Getenv("BD_DEBUG") != "" {
		// 		fmt.Fprintf(os.Stderr, "Debug: failed to check if %s should skip: %v\n", issue.ID, err)
		// 	}
		// 	skip = false
		// }
		// if skip {
		// 	skippedCount++
		// 	continue
		// }
		
		if err := encoder.Encode(issue); err != nil {
			return nil, fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
		
		// DISABLED: export hash tracking (bd-160)
		// contentHash, err := computeIssueContentHash(issue)
		// if err != nil {
		// 	if os.Getenv("BD_DEBUG") != "" {
		// 		fmt.Fprintf(os.Stderr, "Debug: failed to compute hash for %s: %v\n", issue.ID, err)
		// 	}
		// } else if err := store.SetExportHash(ctx, issue.ID, contentHash); err != nil {
		// 	if os.Getenv("BD_DEBUG") != "" {
		// 		fmt.Fprintf(os.Stderr, "Debug: failed to save export hash for %s: %v\n", issue.ID, err)
		// 	}
		// }
		
		exportedIDs = append(exportedIDs, issue.ID)
	}
	
	// Report skipped issues if any (helps debugging bd-159)
	if skippedCount > 0 && os.Getenv("BD_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "Debug: auto-flush skipped %d issue(s) with timestamp-only changes\n", skippedCount)
	}

	// Close temp file before renaming
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temp file: %w", err)
	}
	f = nil // Prevent defer cleanup

	// Atomic rename
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		_ = os.Remove(tempPath) // Clean up on rename failure
		return nil, fmt.Errorf("failed to rename file: %w", err)
	}

	// Set appropriate file permissions (0644: rw-r--r--)
	// nolint:gosec // G302: JSONL needs to be readable by other tools
	if err := os.Chmod(jsonlPath, 0644); err != nil {
		// Non-fatal - file is already written
		if os.Getenv("BD_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Debug: failed to set file permissions: %v\n", err)
		}
	}

	return exportedIDs, nil
}

// flushToJSONL exports dirty issues to JSONL using incremental updates
// flushToJSONL exports dirty database changes to the JSONL file. Uses incremental
// export by default (only exports modified issues), or full export for ID-changing
// operations (e.g., rename-prefix). Invoked by the debounce timer or
// immediately on command exit.
//
// Export modes:
//   - Incremental (default): Exports only GetDirtyIssues(), merges with existing JSONL
//   - Full (after rename-prefix): Exports all issues, rebuilds JSONL from scratch
//
// Error handling: Tracks consecutive failures. After 3+ failures, displays prominent
// warning suggesting manual "bd export" to recover. Failure counter resets on success.
//
// Thread-safety:
//   - Protected by flushMutex for isDirty/needsFullExport access
//   - Checks storeActive flag (via storeMutex) to prevent use-after-close
//   - Safe to call from timer goroutine or main thread
//
// No-op conditions:
//   - Store already closed (storeActive=false)
//   - Database not dirty (isDirty=false)
//   - No dirty issues found (incremental mode only)
func flushToJSONL() {
	// Check if store is still active (not closed)
	storeMutex.Lock()
	if !storeActive {
		storeMutex.Unlock()
		return
	}
	storeMutex.Unlock()

	jsonlPath := findJSONLPath()

	// Double-check store is still active before accessing
	storeMutex.Lock()
	if !storeActive {
		storeMutex.Unlock()
		return
	}
	storeMutex.Unlock()

	ctx := context.Background()
	
	// Validate JSONL integrity BEFORE checking isDirty (bd-c6cf)
	// This detects if JSONL and export_hashes are out of sync (e.g., after git operations)
	// If export_hashes was cleared, we need to do a full export even if nothing is dirty
	integrityNeedsFullExport, err := validateJSONLIntegrity(ctx, jsonlPath)
	if err != nil {
		// Special case: missing JSONL is not fatal, just forces full export (bd-c6cf)
		if !os.IsNotExist(err) {
			// Record failure without clearing isDirty (we didn't do any work yet)
			flushMutex.Lock()
			flushFailureCount++
			lastFlushError = err
			failCount := flushFailureCount
			flushMutex.Unlock()

			// Always show the immediate warning
			fmt.Fprintf(os.Stderr, "Warning: auto-flush failed: %v\n", err)

			// Show prominent warning after 3+ consecutive failures
			if failCount >= 3 {
				red := color.New(color.FgRed, color.Bold).SprintFunc()
				fmt.Fprintf(os.Stderr, "\n%s\n", red("⚠️  CRITICAL: Auto-flush has failed "+fmt.Sprint(failCount)+" times consecutively!"))
				fmt.Fprintf(os.Stderr, "%s\n", red("⚠️  Your JSONL file may be out of sync with the database."))
				fmt.Fprintf(os.Stderr, "%s\n\n", red("⚠️  Run 'bd export -o .beads/issues.jsonl' manually to fix."))
			}
			return
		}
		// Missing JSONL: treat as "force full export" case
		integrityNeedsFullExport = true
	}

	// Now check if we should proceed with export
	flushMutex.Lock()
	if !isDirty && !integrityNeedsFullExport {
		// Nothing to do: no dirty issues and no integrity issue
		flushMutex.Unlock()
		return
	}
	
	// We're proceeding with export - capture state and clear flags
	isDirty = false
	fullExport := needsFullExport || integrityNeedsFullExport
	needsFullExport = false // Reset flag
	flushMutex.Unlock()

	// Helper to record failure
	recordFailure := func(err error) {
		flushMutex.Lock()
		flushFailureCount++
		lastFlushError = err
		failCount := flushFailureCount
		flushMutex.Unlock()

		// Always show the immediate warning
		fmt.Fprintf(os.Stderr, "Warning: auto-flush failed: %v\n", err)

		// Show prominent warning after 3+ consecutive failures
		if failCount >= 3 {
			red := color.New(color.FgRed, color.Bold).SprintFunc()
			fmt.Fprintf(os.Stderr, "\n%s\n", red("⚠️  CRITICAL: Auto-flush has failed "+fmt.Sprint(failCount)+" times consecutively!"))
			fmt.Fprintf(os.Stderr, "%s\n", red("⚠️  Your JSONL file may be out of sync with the database."))
			fmt.Fprintf(os.Stderr, "%s\n\n", red("⚠️  Run 'bd export -o .beads/issues.jsonl' manually to fix."))
		}
	}

	// Helper to record success
	recordSuccess := func() {
		flushMutex.Lock()
		flushFailureCount = 0
		lastFlushError = nil
		flushMutex.Unlock()
	}

	// Determine which issues to export
	var dirtyIDs []string

	if fullExport {
		// Full export: get ALL issues (needed after ID-changing operations like renumber)
		allIssues, err2 := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err2 != nil {
			recordFailure(fmt.Errorf("failed to get all issues: %w", err2))
			return
		}
		dirtyIDs = make([]string, len(allIssues))
		for i, issue := range allIssues {
			dirtyIDs[i] = issue.ID
		}
	} else {
		// Incremental export: get only dirty issue IDs (bd-39 optimization)
		var err2 error
		dirtyIDs, err2 = store.GetDirtyIssues(ctx)
		if err2 != nil {
			recordFailure(fmt.Errorf("failed to get dirty issues: %w", err2))
			return
		}

		// No dirty issues? Nothing to do!
		if len(dirtyIDs) == 0 {
			recordSuccess()
			return
		}
	}

	// Read existing JSONL into a map (skip for full export - we'll rebuild from scratch)
	issueMap := make(map[string]*types.Issue)
	if !fullExport {
		if existingFile, err := os.Open(jsonlPath); err == nil {
			scanner := bufio.NewScanner(existingFile)
			// Increase buffer to handle large JSON lines (bd-c6cf)
			// Default scanner limit is 64KB which can cause silent truncation
			scanner.Buffer(make([]byte, 0, 1024), 2*1024*1024) // 2MB max line size
			lineNum := 0
			for scanner.Scan() {
				lineNum++
				line := scanner.Text()
				if line == "" {
					continue
				}
				var issue types.Issue
				if err := json.Unmarshal([]byte(line), &issue); err == nil {
					issueMap[issue.ID] = &issue
				} else {
					// Warn about malformed JSONL lines
					fmt.Fprintf(os.Stderr, "Warning: skipping malformed JSONL line %d: %v\n", lineNum, err)
				}
			}
			// Check for scanner errors (bd-c6cf)
			if err := scanner.Err(); err != nil {
				_ = existingFile.Close()
				recordFailure(fmt.Errorf("failed to read existing JSONL: %w", err))
				return
			}
			_ = existingFile.Close()
		}
	}

	// Fetch only dirty issues from DB
	for _, issueID := range dirtyIDs {
		issue, err := store.GetIssue(ctx, issueID)
		if err != nil {
			recordFailure(fmt.Errorf("failed to get issue %s: %w", issueID, err))
			return
		}
		if issue == nil {
			// Issue was deleted, remove from map
			delete(issueMap, issueID)
			continue
		}

		// Get dependencies for this issue
		deps, err := store.GetDependencyRecords(ctx, issueID)
		if err != nil {
			recordFailure(fmt.Errorf("failed to get dependencies for %s: %w", issueID, err))
			return
		}
		issue.Dependencies = deps

		// Update map
		issueMap[issueID] = issue
	}

	// Convert map to slice (will be sorted by writeJSONLAtomic)
	issues := make([]*types.Issue, 0, len(issueMap))
	for _, issue := range issueMap {
		issues = append(issues, issue)
	}

	// Write atomically using common helper
	exportedIDs, err := writeJSONLAtomic(jsonlPath, issues)
	if err != nil {
		recordFailure(err)
		return
	}

	// Clear only the dirty issues that were actually exported (fixes bd-52 race condition, bd-159)
	// Don't clear issues that were skipped due to timestamp-only changes
	if len(exportedIDs) > 0 {
		if err := store.ClearDirtyIssuesByID(ctx, exportedIDs); err != nil {
			// Don't fail the whole flush for this, but warn
			fmt.Fprintf(os.Stderr, "Warning: failed to clear dirty issues: %v\n", err)
		}
	}

	// Store hash of exported JSONL (fixes bd-84: enables hash-based auto-import)
	jsonlData, err := os.ReadFile(jsonlPath)
	if err == nil {
		hasher := sha256.New()
		hasher.Write(jsonlData)
		exportedHash := hex.EncodeToString(hasher.Sum(nil))
		if err := store.SetMetadata(ctx, "last_import_hash", exportedHash); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to update last_import_hash after export: %v\n", err)
		}
		
		// Store JSONL file hash for integrity validation (bd-160)
		if err := store.SetJSONLFileHash(ctx, exportedHash); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to update jsonl_file_hash after export: %v\n", err)
		}
	}

	// Success!
	recordSuccess()
}
