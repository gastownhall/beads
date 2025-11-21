package main

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// isJSONLNewer checks if JSONL file is newer than database file.
// Returns true if JSONL is newer, false otherwise.
func isJSONLNewer(jsonlPath string) bool {
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

	return jsonlInfo.ModTime().After(dbInfo.ModTime())
}

// validatePreExport performs integrity checks before exporting database to JSONL.
// Returns error if critical issues found that would cause data loss.
func validatePreExport(ctx context.Context, store storage.Storage, jsonlPath string) error {
	// Check if JSONL is newer than database - if so, must import first
	if isJSONLNewer(jsonlPath) {
		return fmt.Errorf("refusing to export: JSONL is newer than database (import first to avoid data loss)")
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

	// Warning: large divergence suggests sync failure
	if jsonlCount > 0 {
		divergencePercent := math.Abs(float64(dbCount-jsonlCount)) / float64(jsonlCount) * 100
		if divergencePercent > 50 {
			fmt.Fprintf(os.Stderr, "WARNING: DB has %d issues, JSONL has %d (%.1f%% divergence)\n",
				dbCount, jsonlCount, divergencePercent)
			fmt.Fprintf(os.Stderr, "This suggests sync failure - investigate before proceeding\n")
		}
	}

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
// Returns error if issue count decreased (data loss) or nil if OK.
func validatePostImport(before, after int) error {
	if after < before {
		return fmt.Errorf("import reduced issue count: %d → %d (data loss detected!)", before, after)
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
