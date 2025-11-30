package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/deletions"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// isJSONLNewer checks if JSONL file is newer than database file.
// Returns true if JSONL is newer AND has different content, false otherwise.
// This prevents false positives from daemon auto-export timestamp skew (bd-lm2q).
//
// NOTE: This uses computeDBHash which is more expensive than hasJSONLChanged.
// For daemon auto-import, prefer hasJSONLChanged() which uses metadata-based
// content tracking and is safe against git operations (bd-khnb).
func isJSONLNewer(jsonlPath string) bool {
	return isJSONLNewerWithStore(jsonlPath, nil)
}

// isJSONLNewerWithStore is like isJSONLNewer but accepts an optional store parameter.
// If st is nil, it will try to use the global store.
func isJSONLNewerWithStore(jsonlPath string, st storage.Storage) bool {
	jsonlInfo, jsonlStatErr := os.Stat(jsonlPath)
	if jsonlStatErr != nil {
		return false
	}

	beadsDir := filepath.Dir(jsonlPath)
	dbPath := filepath.Join(beadsDir, "beads.db")
	dbInfo, dbStatErr := os.Stat(dbPath)
	if dbStatErr != nil {
		return false
	}

	// Quick path: if DB is newer, JSONL is definitely not newer
	if !jsonlInfo.ModTime().After(dbInfo.ModTime()) {
		return false
	}

	// JSONL is newer by timestamp - but this could be due to daemon auto-export
	// or clock skew. Use content-based comparison to determine if import is needed.
	// If we can't determine content hash (e.g., store not available), conservatively
	// assume JSONL is newer to trigger auto-import.
	if st == nil {
		if ensureStoreActive() != nil || store == nil {
			return true // Conservative: can't check content, assume different
		}
		st = store
	}

	ctx := context.Background()
	jsonlHash, err := computeJSONLHash(jsonlPath)
	if err != nil {
		return true // Conservative: can't read JSONL, assume different
	}

	dbHash, err := computeDBHash(ctx, st)
	if err != nil {
		return true // Conservative: can't read DB, assume different
	}

	// Compare hashes: if they match, JSONL and DB have same content
	// despite timestamp difference (daemon auto-export case)
	return jsonlHash != dbHash
}

// computeJSONLHash computes SHA256 hash of JSONL file content.
// Returns hex-encoded hash string and any error encountered reading the file.
func computeJSONLHash(jsonlPath string) (string, error) {
	jsonlData, err := os.ReadFile(jsonlPath) // #nosec G304 - controlled path
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	hasher.Write(jsonlData)
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// hasJSONLChanged checks if JSONL content has changed since last import using SHA256 hash.
// Returns true if JSONL content differs from last import, false otherwise.
// This is safe against git operations that restore old files with recent mtimes.
//
// Performance optimization: Checks mtime first as a fast-path. Only computes expensive
// SHA256 hash if mtime changed. This makes 99% of checks instant (mtime unchanged = content
// unchanged) while still catching git operations that restore old content with new mtimes.
//
// In multi-repo mode, keySuffix should be the stable repo identifier (e.g., ".", "../frontend").
// The keySuffix must not contain the ':' separator character (bd-ar2.12).
func hasJSONLChanged(ctx context.Context, store storage.Storage, jsonlPath string, keySuffix string) bool {
	// Validate keySuffix doesn't contain the separator character (bd-ar2.12)
	if keySuffix != "" && strings.Contains(keySuffix, ":") {
		// Invalid keySuffix - treat as changed to trigger proper error handling
		return true
	}

	// Build metadata keys with optional suffix for per-repo tracking (bd-ar2.10, bd-ar2.11)
	// Renamed from last_import_hash to jsonl_content_hash (bd-39o) - more accurate name
	// since this hash is updated on both import AND export
	hashKey := "jsonl_content_hash"
	oldHashKey := "last_import_hash" // Migration: check old key if new key missing
	if keySuffix != "" {
		hashKey += ":" + keySuffix
		oldHashKey += ":" + keySuffix
	}

	// Always compute content hash (bd-v0y fix)
	// Previous mtime-based fast-path was unsafe: git operations (pull, checkout, rebase)
	// can change file content without updating mtime, causing false negatives.
	// Hash computation is fast enough for sync operations (~10-50ms even for large DBs).
	currentHash, err := computeJSONLHash(jsonlPath)
	if err != nil {
		// If we can't read JSONL, assume no change (don't auto-import broken files)
		return false
	}

	// Get content hash from metadata (try new key first, fall back to old for migration)
	lastHash, err := store.GetMetadata(ctx, hashKey)
	if err != nil || lastHash == "" {
		// Try old key for migration (bd-39o)
		lastHash, err = store.GetMetadata(ctx, oldHashKey)
		if err != nil || lastHash == "" {
			// No previous hash - this is the first run or metadata is missing
			// Assume changed to trigger import
			return true
		}
	}

	// Compare hashes
	return currentHash != lastHash
}

// validatePreExport performs integrity checks before exporting database to JSONL.
// Returns error if critical issues found that would cause data loss.
func validatePreExport(ctx context.Context, store storage.Storage, jsonlPath string) error {
	// Check if JSONL content has changed since last import - if so, must import first
	// Uses content-based detection (bd-xwo fix) instead of mtime-based to avoid false positives from git operations
	// Use getRepoKeyForPath to get stable repo identifier for multi-repo support (bd-ar2.10, bd-ar2.11)
	repoKey := getRepoKeyForPath(jsonlPath)
	if hasJSONLChanged(ctx, store, jsonlPath, repoKey) {
		return fmt.Errorf("refusing to export: JSONL content has changed since last import (import first to avoid data loss)")
	}

	jsonlInfo, jsonlStatErr := os.Stat(jsonlPath)

	// Get database issue count (fast path with COUNT(*) if available)
	dbCount, err := countDBIssuesFast(ctx, store)
	if err != nil {
		return fmt.Errorf("failed to count database issues: %w", err)
	}

	// Get JSONL issue count
	jsonlCount := 0
	if jsonlStatErr == nil {
		jsonlCount, err = countIssuesInJSONL(jsonlPath)
		if err != nil {
			// Conservative: if JSONL exists with content but we can't count it,
			// and DB is empty, refuse to export (potential data loss)
			if dbCount == 0 && jsonlInfo.Size() > 0 {
				return fmt.Errorf("refusing to export empty DB over existing JSONL whose contents couldn't be verified: %w", err)
			}
			// Warning for other cases
			fmt.Fprintf(os.Stderr, "WARNING: Failed to count issues in JSONL: %v\n", err)
		}
	}

	// Critical: refuse to export empty DB over non-empty JSONL
	if dbCount == 0 && jsonlCount > 0 {
		return fmt.Errorf("refusing to export empty DB over %d issues in JSONL (would cause data loss)", jsonlCount)
	}

	// Note: The main bd-53c protection is the reverse ZFC check in sync.go
	// which runs BEFORE this validation. Here we only block empty DB.
	// This allows legitimate deletions while sync.go catches stale DBs.

	return nil
}

// checkDuplicateIDs detects duplicate issue IDs in the database.
// Returns error if duplicates are found (indicates database corruption).
func checkDuplicateIDs(ctx context.Context, store storage.Storage) error {
	// Get access to underlying database
	// This is a hack - we need to add a proper interface method for this
	// For now, we'll use a type assertion to access the underlying *sql.DB
	type dbGetter interface {
		GetDB() interface{}
	}

	getter, ok := store.(dbGetter)
	if !ok {
		// If store doesn't expose GetDB, skip this check
		// This is acceptable since duplicate IDs are prevented by UNIQUE constraint
		return nil
	}

	db, ok := getter.GetDB().(*sql.DB)
	if !ok || db == nil {
		return nil
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, COUNT(*) as cnt 
		FROM issues 
		GROUP BY id 
		HAVING cnt > 1
	`)
	if err != nil {
		return fmt.Errorf("failed to check for duplicate IDs: %w", err)
	}
	defer rows.Close()

	var duplicates []string
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return fmt.Errorf("failed to scan duplicate ID row: %w", err)
		}
		duplicates = append(duplicates, fmt.Sprintf("%s (x%d)", id, count))
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating duplicate IDs: %w", err)
	}

	if len(duplicates) > 0 {
		return fmt.Errorf("database corruption: duplicate IDs: %v", duplicates)
	}

	return nil
}

// checkOrphanedDeps finds dependencies pointing to or from non-existent issues.
// Returns list of orphaned dependency IDs and any error encountered.
func checkOrphanedDeps(ctx context.Context, store storage.Storage) ([]string, error) {
	// Get access to underlying database
	type dbGetter interface {
		GetDB() interface{}
	}

	getter, ok := store.(dbGetter)
	if !ok {
		return nil, nil
	}

	db, ok := getter.GetDB().(*sql.DB)
	if !ok || db == nil {
		return nil, nil
	}

	// Check both sides: dependencies where either issue_id or depends_on_id doesn't exist
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT d.issue_id 
		FROM dependencies d 
		LEFT JOIN issues i ON d.issue_id = i.id 
		WHERE i.id IS NULL
		UNION
		SELECT DISTINCT d.depends_on_id 
		FROM dependencies d 
		LEFT JOIN issues i ON d.depends_on_id = i.id 
		WHERE i.id IS NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to check for orphaned dependencies: %w", err)
	}
	defer rows.Close()

	var orphaned []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan orphaned dependency: %w", err)
		}
		orphaned = append(orphaned, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating orphaned dependencies: %w", err)
	}

	if len(orphaned) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: Found %d orphaned dependency references: %v\n", len(orphaned), orphaned)
	}

	return orphaned, nil
}

// validatePostImport checks that import didn't cause data loss.
// Returns error if issue count decreased unexpectedly (data loss) or nil if OK.
// A decrease is legitimate if it matches deletions recorded in deletions.jsonl.
//
// Parameters:
//   - before: issue count in DB before import
//   - after: issue count in DB after import
//   - jsonlPath: path to issues.jsonl (used to locate deletions.jsonl)
func validatePostImport(before, after int, jsonlPath string) error {
	return validatePostImportWithExpectedDeletions(before, after, 0, jsonlPath)
}

// validatePostImportWithExpectedDeletions checks that import didn't cause data loss,
// accounting for expected deletions that were already sanitized from the JSONL.
// Returns error if issue count decreased unexpectedly (data loss) or nil if OK.
//
// Parameters:
//   - before: issue count in DB before import
//   - after: issue count in DB after import
//   - expectedDeletions: number of issues known to have been deleted (from sanitize step)
//   - jsonlPath: path to issues.jsonl (used to locate deletions.jsonl)
func validatePostImportWithExpectedDeletions(before, after, expectedDeletions int, jsonlPath string) error {
	if after < before {
		// Count decrease - check if this matches legitimate deletions
		decrease := before - after

		// First, account for expected deletions from the sanitize step (bd-tt0 fix)
		// These were already removed from JSONL and will be purged from DB by import
		if expectedDeletions > 0 && decrease <= expectedDeletions {
			// Decrease is fully accounted for by expected deletions
			fmt.Fprintf(os.Stderr, "Import complete: %d → %d issues (-%d, expected from sanitize)\n",
				before, after, decrease)
			return nil
		}

		// If decrease exceeds expected deletions, check deletions manifest for additional legitimacy
		unexplainedDecrease := decrease - expectedDeletions

		// Load deletions manifest to check for legitimate deletions
		beadsDir := filepath.Dir(jsonlPath)
		deletionsPath := deletions.DefaultPath(beadsDir)
		loadResult, err := deletions.LoadDeletions(deletionsPath)
		if err != nil {
			// If we can't load deletions, assume the worst
			return fmt.Errorf("import reduced issue count: %d → %d (data loss detected! failed to verify deletions: %v)", before, after, err)
		}

		// If there are deletions recorded, the decrease is likely legitimate
		// We can't perfectly match because we don't know exactly which issues
		// were deleted in this sync cycle vs previously. But if there are ANY
		// deletions recorded and the decrease is reasonable, allow it.
		numDeletions := len(loadResult.Records)
		if numDeletions > 0 && unexplainedDecrease <= numDeletions {
			// Legitimate deletion - decrease is accounted for by deletions manifest
			if expectedDeletions > 0 {
				fmt.Fprintf(os.Stderr, "Import complete: %d → %d issues (-%d, %d from sanitize + %d from deletions manifest)\n",
					before, after, decrease, expectedDeletions, unexplainedDecrease)
			} else {
				fmt.Fprintf(os.Stderr, "Import complete: %d → %d issues (-%d, accounted for by %d deletion(s))\n",
					before, after, decrease, numDeletions)
			}
			return nil
		}

		// Decrease exceeds recorded deletions - potential data loss
		if numDeletions > 0 || expectedDeletions > 0 {
			return fmt.Errorf("import reduced issue count: %d → %d (-%d exceeds %d expected + %d recorded deletion(s) - potential data loss!)",
				before, after, decrease, expectedDeletions, numDeletions)
		}
		return fmt.Errorf("import reduced issue count: %d → %d (data loss detected! no deletions recorded)", before, after)
	}
	if after == before {
		fmt.Fprintf(os.Stderr, "Import complete: no changes\n")
	} else {
		fmt.Fprintf(os.Stderr, "Import complete: %d → %d issues (+%d)\n", before, after, after-before)
	}
	return nil
}

// countDBIssues returns the total number of issues in the database.
// This is the legacy interface kept for compatibility.
func countDBIssues(ctx context.Context, store storage.Storage) (int, error) {
	return countDBIssuesFast(ctx, store)
}

// countDBIssuesFast uses COUNT(*) if possible, falls back to SearchIssues.
func countDBIssuesFast(ctx context.Context, store storage.Storage) (int, error) {
	// Try fast path with COUNT(*) using direct SQL
	// This is a hack until we add a proper CountIssues method to storage.Storage
	type dbGetter interface {
		GetDB() interface{}
	}

	if getter, ok := store.(dbGetter); ok {
		if db, ok := getter.GetDB().(*sql.DB); ok && db != nil {
			var count int
			err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count)
			if err == nil {
				return count, nil
			}
			// Fall through to slow path on error
		}
	}

	// Fallback: load all issues and count them (slow but always works)
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return 0, fmt.Errorf("failed to count database issues: %w", err)
	}
	return len(issues), nil
}

// dbNeedsExport checks if the database has changes that differ from JSONL.
// Returns true if export is needed, false if DB and JSONL are already in sync.
func dbNeedsExport(ctx context.Context, store storage.Storage, jsonlPath string) (bool, error) {
	// Check if JSONL exists
	jsonlInfo, err := os.Stat(jsonlPath)
	if os.IsNotExist(err) {
		// JSONL doesn't exist - always need to export
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to stat JSONL: %w", err)
	}

	// Check database modification time
	beadsDir := filepath.Dir(jsonlPath)
	dbPath := filepath.Join(beadsDir, "beads.db")
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		return false, fmt.Errorf("failed to stat database: %w", err)
	}

	// If database is newer than JSONL, we need to export
	if dbInfo.ModTime().After(jsonlInfo.ModTime()) {
		return true, nil
	}

	// If modification times suggest they're in sync, verify counts match
	dbCount, err := countDBIssuesFast(ctx, store)
	if err != nil {
		return false, fmt.Errorf("failed to count database issues: %w", err)
	}

	jsonlCount, err := countIssuesInJSONL(jsonlPath)
	if err != nil {
		return false, fmt.Errorf("failed to count JSONL issues: %w", err)
	}

	// If counts don't match, we need to export
	if dbCount != jsonlCount {
		return true, nil
	}

	// DB and JSONL appear to be in sync
	return false, nil
}

// computeDBHash computes a content hash of the database by exporting to memory.
// This is used to compare DB content with JSONL content without relying on timestamps.
func computeDBHash(ctx context.Context, store storage.Storage) (string, error) {
	// Get all issues from DB
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return "", fmt.Errorf("failed to get issues: %w", err)
	}

	// Sort by ID for consistent hash
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].ID < issues[j].ID
	})

	// Populate dependencies
	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get dependencies: %w", err)
	}
	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
	}

	// Populate labels
	for _, issue := range issues {
		labels, err := store.GetLabels(ctx, issue.ID)
		if err != nil {
			return "", fmt.Errorf("failed to get labels for %s: %w", issue.ID, err)
		}
		issue.Labels = labels
	}

	// Populate comments
	for _, issue := range issues {
		comments, err := store.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return "", fmt.Errorf("failed to get comments for %s: %w", issue.ID, err)
		}
		issue.Comments = comments
	}

	// Serialize to JSON and hash
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			return "", fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
	}

	hasher := sha256.New()
	hasher.Write(buf.Bytes())
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
