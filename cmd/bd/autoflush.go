package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
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

// outputJSONError outputs an error as JSON to stderr and exits with code 1.
// Use this when jsonOutput is true and an error occurs, to ensure consistent
// machine-readable error output. The error is formatted as:
//
//	{"error": "error message", "code": "error_code"}
//
// The code parameter is optional (pass "" to omit).
func outputJSONError(err error, code string) {
	errObj := map[string]string{"error": err.Error()}
	if code != "" {
		errObj["code"] = code
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(errObj)
	os.Exit(1)
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
	// Allow explicit override (useful in no-db mode or non-standard layouts)
	if jsonlEnv := os.Getenv("BEADS_JSONL"); jsonlEnv != "" {
		return utils.CanonicalizePath(jsonlEnv)
	}

	// Use public API for path discovery
	jsonlPath := beads.FindJSONLPath(dbPath)

	// In --no-db mode, dbPath may be empty. Fall back to locating the .beads directory.
	if jsonlPath == "" {
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			return ""
		}
		jsonlPath = utils.FindJSONLInDir(beadsDir)
	}

	// Ensure the directory exists (important for new databases)
	// This is the only difference from the public API - we create the directory
	dbDir := filepath.Dir(jsonlPath)
	if err := os.MkdirAll(dbDir, 0750); err != nil {
		// If we can't create the directory, return discovered path anyway
		// (the subsequent write will fail with a clearer error)
		return jsonlPath
	}

	return jsonlPath
}

// autoImportIfNewer checks if JSONL content changed (via hash) and imports if so
// Hash-based comparison is git-proof (mtime comparison fails after git pull).
// Uses collision detection to prevent silently overwriting local changes.
// Defense-in-depth check to respect --no-auto-import flag.
func autoImportIfNewer() {
	// Defense-in-depth: always check noAutoImport flag directly
	// This ensures auto-import is disabled even if caller forgot to check autoImportEnabled
	if noAutoImport {
		debug.Logf("auto-import skipped (--no-auto-import flag)")
		return
	}

	// Find JSONL path
	jsonlPath := findJSONLPath()

	// Read JSONL file
	jsonlData, err := os.ReadFile(jsonlPath)
	if err != nil {
		// JSONL doesn't exist or can't be accessed, skip import
		debug.Logf("auto-import skipped, JSONL not found: %v", err)
		return
	}

	// Compute current JSONL hash
	hasher := sha256.New()
	hasher.Write(jsonlData)
	currentHash := hex.EncodeToString(hasher.Sum(nil))

	// Get content hash from DB metadata (try new key first, fall back to old for migration)
	ctx := rootCtx
	lastHash, err := store.GetMetadata(ctx, "jsonl_content_hash")
	if err != nil || lastHash == "" {
		lastHash, err = store.GetMetadata(ctx, "last_import_hash")
		if err != nil {
			// Metadata error - treat as first import rather than skipping
			// This allows auto-import to recover from corrupt/missing metadata
			debug.Logf("metadata read failed (%v), treating as first import", err)
			lastHash = ""
		}
	}

	// Compare hashes
	if currentHash == lastHash {
		// Content unchanged, skip import
		debug.Logf("auto-import skipped, JSONL unchanged (hash match)")
		return
	}

	debug.Logf("auto-import triggered (hash changed)")

	// Check for Git merge conflict markers
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
		issue.SetDefaults() // Apply defaults for omitted fields (beads-399)

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

	// Clear export_hashes before import to prevent staleness
	// Import operations may add/update issues, so export_hashes entries become invalid
	if err := store.ClearAllExportHashes(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to clear export_hashes before import: %v\n", err)
	}
	
	// Use shared import logic
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
		slices.SortFunc(mappings, func(a, b mapping) int {
			return cmp.Compare(a.oldID, b.oldID)
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

	// Store new hash after successful import (renamed from last_import_hash)
	if err := store.SetMetadata(ctx, "jsonl_content_hash", currentHash); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update jsonl_content_hash after import: %v\n", err)
		fmt.Fprintf(os.Stderr, "This may cause auto-import to retry the same import on next operation.\n")
	}

	// Store import timestamp for staleness detection
	// Use RFC3339Nano for nanosecond precision to avoid race with file mtime
	importTime := time.Now().Format(time.RFC3339Nano)
	if err := store.SetMetadata(ctx, "last_import_time", importTime); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update last_import_time after import: %v\n", err)
	}
}



// markDirtyAndScheduleFlush marks the database as dirty and schedules a flush
// markDirtyAndScheduleFlush marks the database as dirty and schedules a debounced
// export to JSONL. Uses FlushManager's event-driven architecture.
//
// Debouncing behavior: If multiple operations happen within the debounce window, only
// one flush occurs after the burst of activity completes. This prevents excessive
// writes during rapid issue creation/updates.
//
// Flush-on-exit guarantee: PersistentPostRun calls flushManager.Shutdown() which
// performs a final flush before the command exits, ensuring no data is lost.
//
// Thread-safe: Safe to call from multiple goroutines (no shared mutable state).
// No-op if auto-flush is disabled via --no-auto-flush flag.
func markDirtyAndScheduleFlush() {
	// Use FlushManager if available
	// No FlushManager means sandbox mode or test without flush setup - no-op is correct
	if flushManager != nil {
		flushManager.MarkDirty(false) // Incremental export
	}
}

// markDirtyAndScheduleFullExport marks DB as needing a full export (for ID-changing operations)
func markDirtyAndScheduleFullExport() {
	// Use FlushManager if available
	// No FlushManager means sandbox mode or test without flush setup - no-op is correct
	if flushManager != nil {
		flushManager.MarkDirty(true) // Full export
	}
}

// clearAutoFlushState cancels pending flush and marks DB as clean (after manual export)
func clearAutoFlushState() {
	// With FlushManager, clearing state is unnecessary
	// If a flush is pending and fires after manual export, flushToJSONLWithState()
	// will detect nothing is dirty and skip the flush. This is harmless.
	// Reset failure counters on manual export success
	flushMutex.Lock()
	flushFailureCount = 0
	lastFlushError = nil
	flushMutex.Unlock()
}

// writeJSONLAtomic writes issues to a JSONL file atomically using temp file + rename.
// This is the common implementation used by flushToJSONLWithState (SQLite mode) and
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
// If mismatch detected, clears export_hashes and logs warning.
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
			// JSONL doesn't exist but we have a stored hash - clear export_hashes and jsonl_file_hash
			fmt.Fprintf(os.Stderr, "⚠️  WARNING: JSONL file missing but export_hashes exist. Clearing export_hashes.\n")
			if err := store.ClearAllExportHashes(ctx); err != nil {
				return false, fmt.Errorf("failed to clear export_hashes: %w", err)
			}
			// Also clear jsonl_file_hash to prevent perpetual mismatch warnings
			if err := store.SetJSONLFileHash(ctx, ""); err != nil {
				return false, fmt.Errorf("failed to clear jsonl_file_hash: %w", err)
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
		fmt.Fprintf(os.Stderr, "⚠️  WARNING: JSONL file hash mismatch detected\n")
		fmt.Fprintf(os.Stderr, "  This indicates JSONL and export_hashes are out of sync.\n")
		fmt.Fprintf(os.Stderr, "  Clearing export_hashes to force full re-export.\n")

		// Clear export_hashes to force full re-export
		if err := store.ClearAllExportHashes(ctx); err != nil {
			return false, fmt.Errorf("failed to clear export_hashes: %w", err)
		}
		// Also clear jsonl_file_hash to prevent perpetual mismatch warnings
		if err := store.SetJSONLFileHash(ctx, ""); err != nil {
			return false, fmt.Errorf("failed to clear jsonl_file_hash: %w", err)
		}
		return true, nil // Signal full export needed
	}
	
	return false, nil
}

func writeJSONLAtomic(jsonlPath string, issues []*types.Issue) ([]string, error) {
	// Sort issues by ID for consistent output
	slices.SortFunc(issues, func(a, b *types.Issue) int {
		return cmp.Compare(a.ID, b.ID)
	})

	// Create temp file with PID suffix to avoid collisions
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

	// Write all issues as JSONL (timestamp-only deduplication DISABLED)
	encoder := json.NewEncoder(f)
	skippedCount := 0
	exportedIDs := make([]string, 0, len(issues))
	
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
		 return nil, fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
		
		exportedIDs = append(exportedIDs, issue.ID)
	}
	
	// Report skipped issues if any (helps debugging)
	if skippedCount > 0 {
		debug.Logf("auto-flush skipped %d issue(s) with timestamp-only changes", skippedCount)
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
		debug.Logf("failed to set file permissions: %v", err)
	}

	return exportedIDs, nil
}

// recordFlushFailure records a flush failure, incrementing the failure counter
// and displaying warnings after consecutive failures.
func recordFlushFailure(err error) {
	flushMutex.Lock()
	flushFailureCount++
	lastFlushError = err
	failCount := flushFailureCount
	flushMutex.Unlock()

	// Always show the immediate warning
	fmt.Fprintf(os.Stderr, "Warning: auto-flush failed: %v\n", err)

	// Show prominent warning after 3+ consecutive failures
	if failCount >= 3 {
		fmt.Fprintf(os.Stderr, "\n%s\n", ui.RenderFail("⚠️  CRITICAL: Auto-flush has failed "+fmt.Sprint(failCount)+" times consecutively!"))
		fmt.Fprintf(os.Stderr, "%s\n", ui.RenderFail("⚠️  Your JSONL file may be out of sync with the database."))
		fmt.Fprintf(os.Stderr, "%s\n\n", ui.RenderFail("⚠️  Run 'bd export -o .beads/issues.jsonl' manually to fix."))
	}
}

// recordFlushSuccess records a successful flush, resetting the failure counter.
func recordFlushSuccess() {
	flushMutex.Lock()
	flushFailureCount = 0
	lastFlushError = nil
	flushMutex.Unlock()
}

// readExistingJSONL reads an existing JSONL file into a map for incremental merging.
// Returns empty map if file doesn't exist or can't be read.
func readExistingJSONL(jsonlPath string) (map[string]*types.Issue, error) {
	issueMap := make(map[string]*types.Issue)

	existingFile, err := os.Open(jsonlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return issueMap, nil // File doesn't exist, return empty map
		}
		return nil, fmt.Errorf("failed to open existing JSONL: %w", err)
	}
	defer existingFile.Close()

	scanner := bufio.NewScanner(existingFile)
	// Increase buffer to handle large JSON lines
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
			issue.SetDefaults() // Apply defaults for omitted fields (beads-399)
			issueMap[issue.ID] = &issue
		} else {
			// Warn about malformed JSONL lines
			fmt.Fprintf(os.Stderr, "Warning: skipping malformed JSONL line %d: %v\n", lineNum, err)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read existing JSONL: %w", err)
	}

	return issueMap, nil
}

// fetchAndMergeIssues fetches dirty issues from the database and merges them into issueMap.
// Issues that no longer exist are removed from the map.
func fetchAndMergeIssues(ctx context.Context, s storage.Storage, dirtyIDs []string, issueMap map[string]*types.Issue) error {
	for _, issueID := range dirtyIDs {
		issue, err := s.GetIssue(ctx, issueID)
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", issueID, err)
		}
		if issue == nil {
			// Issue was deleted, remove from map
			delete(issueMap, issueID)
			continue
		}

		// Get dependencies for this issue
		deps, err := s.GetDependencyRecords(ctx, issueID)
		if err != nil {
			return fmt.Errorf("failed to get dependencies for %s: %w", issueID, err)
		}
		issue.Dependencies = deps

		// Update map
		issueMap[issueID] = issue
	}
	return nil
}

// filterWisps removes ephemeral (wisp) issues from the map and returns a slice.
// Wisps should never be exported to JSONL.
func filterWisps(issueMap map[string]*types.Issue) []*types.Issue {
	issues := make([]*types.Issue, 0, len(issueMap))
	wispsSkipped := 0
	for _, issue := range issueMap {
		if issue.Ephemeral {
			wispsSkipped++
			continue
		}
		issues = append(issues, issue)
	}
	if wispsSkipped > 0 {
		debug.Logf("auto-flush: filtered %d wisps from export", wispsSkipped)
	}
	return issues
}

// filterByMultiRepoPrefix filters issues by prefix in multi-repo mode.
// Non-primary repos should only export issues matching their own prefix.
func filterByMultiRepoPrefix(ctx context.Context, s storage.Storage, issues []*types.Issue) []*types.Issue {
	multiRepo := config.GetMultiRepoConfig()
	if multiRepo == nil {
		return issues
	}

	// Get our configured prefix
	prefix, prefixErr := s.GetConfig(ctx, "issue_prefix")
	if prefixErr != nil || prefix == "" {
		return issues
	}

	// Determine if we're the primary repo
	cwd, _ := os.Getwd()
	primaryPath := multiRepo.Primary
	if primaryPath == "" || primaryPath == "." {
		primaryPath = cwd
	}

	// Normalize paths for comparison
	absCwd, _ := filepath.Abs(cwd)
	absPrimary, _ := filepath.Abs(primaryPath)

	if absCwd == absPrimary {
		return issues // Primary repo exports all issues
	}

	// Filter to only issues matching our prefix
	filtered := make([]*types.Issue, 0, len(issues))
	prefixWithDash := prefix
	if !strings.HasSuffix(prefixWithDash, "-") {
		prefixWithDash = prefix + "-"
	}
	for _, issue := range issues {
		if strings.HasPrefix(issue.ID, prefixWithDash) {
			filtered = append(filtered, issue)
		}
	}
	debug.Logf("multi-repo filter: %d issues -> %d (prefix %s)", len(issues), len(filtered), prefix)
	return filtered
}

// updateFlushExportMetadata stores hashes and timestamps after a successful flush export.
func updateFlushExportMetadata(ctx context.Context, s storage.Storage, jsonlPath string) {
	jsonlData, err := os.ReadFile(jsonlPath)
	if err != nil {
		return // Non-fatal, just skip metadata update
	}

	hasher := sha256.New()
	hasher.Write(jsonlData)
	exportedHash := hex.EncodeToString(hasher.Sum(nil))

	if err := s.SetMetadata(ctx, "jsonl_content_hash", exportedHash); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update jsonl_content_hash after export: %v\n", err)
	}

	// Store JSONL file hash for integrity validation
	if err := s.SetJSONLFileHash(ctx, exportedHash); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update jsonl_file_hash after export: %v\n", err)
	}

	// Update last_import_time so staleness check doesn't see JSONL as "newer" (fixes #399)
	// Use RFC3339Nano to preserve nanosecond precision.
	exportTime := time.Now().Format(time.RFC3339Nano)
	if err := s.SetMetadata(ctx, "last_import_time", exportTime); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update last_import_time after export: %v\n", err)
	}
}

// flushState captures the state needed for a flush operation
type flushState struct {
	forceDirty      bool // Force flush even if isDirty is false
	forceFullExport bool // Force full export even if needsFullExport is false
}

// flushToJSONLWithState performs the actual flush with explicit state parameters.
// This is the core implementation that doesn't touch global state.
//
// Export modes:
//   - Incremental (default): Exports only GetDirtyIssues(), merges with existing JSONL
//   - Full (forceFullExport=true): Exports all issues, rebuilds JSONL from scratch
//
// Error handling: Tracks consecutive failures. After 3+ failures, displays prominent
// warning suggesting manual "bd export" to recover. Failure counter resets on success.
//
// Thread-safety:
//   - Checks storeActive flag (via storeMutex) to prevent use-after-close
//   - Does NOT modify global isDirty/needsFullExport flags
//   - Safe to call from multiple goroutines
//
// No-op conditions:
//   - Store already closed (storeActive=false)
//   - Database not dirty (isDirty=false) AND forceDirty=false
//   - No dirty issues found (incremental mode only)
func flushToJSONLWithState(state flushState) {
	// Check if store is still active (not closed) and not nil
	storeMutex.Lock()
	if !storeActive || store == nil {
		storeMutex.Unlock()
		return
	}
	storeMutex.Unlock()

	jsonlPath := findJSONLPath()

	// Double-check store is still active before accessing
	storeMutex.Lock()
	if !storeActive || store == nil {
		storeMutex.Unlock()
		return
	}
	storeMutex.Unlock()

	ctx := rootCtx

	// Validate JSONL integrity BEFORE checking isDirty
	// This detects if JSONL and export_hashes are out of sync (e.g., after git operations)
	integrityNeedsFullExport, err := validateJSONLIntegrity(ctx, jsonlPath)
	if err != nil {
		if !os.IsNotExist(err) {
			recordFlushFailure(err)
			return
		}
		// Missing JSONL: treat as "force full export" case
		integrityNeedsFullExport = true
	}

	// Check if we should proceed with export
	if !state.forceDirty && !integrityNeedsFullExport {
		return
	}

	// Determine export mode
	fullExport := state.forceFullExport || integrityNeedsFullExport

	// Determine which issues to export
	dirtyIDs, err := getIssuesToExport(ctx, fullExport)
	if err != nil {
		recordFlushFailure(err)
		return
	}
	if len(dirtyIDs) == 0 && !fullExport {
		recordFlushSuccess()
		return
	}

	// Read existing JSONL into a map (skip for full export - we'll rebuild from scratch)
	var issueMap map[string]*types.Issue
	if fullExport {
		issueMap = make(map[string]*types.Issue)
	} else {
		issueMap, err = readExistingJSONL(jsonlPath)
		if err != nil {
			recordFlushFailure(err)
			return
		}
	}

	// Fetch dirty issues from DB and merge into map
	if err := fetchAndMergeIssues(ctx, store, dirtyIDs, issueMap); err != nil {
		recordFlushFailure(err)
		return
	}

	// Convert map to slice, filtering out wisps
	issues := filterWisps(issueMap)

	// Filter by prefix in multi-repo mode
	issues = filterByMultiRepoPrefix(ctx, store, issues)

	// Write atomically
	exportedIDs, err := writeJSONLAtomic(jsonlPath, issues)
	if err != nil {
		recordFlushFailure(err)
		return
	}

	// Clear dirty issues that were exported
	if len(exportedIDs) > 0 {
		if err := store.ClearDirtyIssuesByID(ctx, exportedIDs); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to clear dirty issues: %v\n", err)
		}
	}

	// Update metadata (hashes, timestamps)
	updateFlushExportMetadata(ctx, store, jsonlPath)

	recordFlushSuccess()
}

// getIssuesToExport determines which issue IDs need to be exported.
// For full export, returns all issue IDs. For incremental, returns only dirty IDs.
func getIssuesToExport(ctx context.Context, fullExport bool) ([]string, error) {
	if fullExport {
		allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			return nil, fmt.Errorf("failed to get all issues: %w", err)
		}
		ids := make([]string, len(allIssues))
		for i, issue := range allIssues {
			ids[i] = issue.ID
		}
		return ids, nil
	}

	dirtyIDs, err := store.GetDirtyIssues(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get dirty issues: %w", err)
	}
	return dirtyIDs, nil
}
