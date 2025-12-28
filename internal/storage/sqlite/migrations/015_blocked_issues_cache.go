package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateBlockedIssuesCache creates the blocked_issues_cache table for performance optimization
// This cache materializes the recursive CTE computation from GetReadyWork to avoid
// expensive recursive queries on every call
func MigrateBlockedIssuesCache(db *sql.DB) error {
	// Check if table already exists
	var tableName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='blocked_issues_cache'
	`).Scan(&tableName)

	if err == sql.ErrNoRows {
		// Create the cache table
		_, err := db.Exec(`
			CREATE TABLE blocked_issues_cache (
				issue_id TEXT NOT NULL,
				PRIMARY KEY (issue_id),
				FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
			)
		`)
		if err != nil {
			return fmt.Errorf("failed to create blocked_issues_cache table: %w", err)
		}

		// Populate the cache with initial data using the existing recursive CTE logic
		_, err = db.Exec(`
			INSERT INTO blocked_issues_cache (issue_id)
			WITH RECURSIVE
			  -- Step 1: Find issues blocked directly by dependencies
			  blocked_directly AS (
			    SELECT DISTINCT d.issue_id
			    FROM dependencies d
			    JOIN issues blocker ON d.depends_on_id = blocker.id
			    WHERE d.type = 'blocks'
			      AND blocker.status IN ('open', 'in_progress', 'blocked')
			  ),

			  -- Step 2: Propagate blockage to all descendants via parent-child
			  blocked_transitively AS (
			    -- Base case: directly blocked issues
			    SELECT issue_id, 0 as depth
			    FROM blocked_directly

			    UNION ALL

			    -- Recursive case: children of blocked issues inherit blockage
			    SELECT d.issue_id, bt.depth + 1
			    FROM blocked_transitively bt
			    JOIN dependencies d ON d.depends_on_id = bt.issue_id
			    WHERE d.type = 'parent-child'
			      AND bt.depth < 50
			  )
			SELECT DISTINCT issue_id FROM blocked_transitively
		`)
		if err != nil {
			return fmt.Errorf("failed to populate blocked_issues_cache: %w", err)
		}

		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check for blocked_issues_cache table: %w", err)
	}

	// Table already exists
	return nil
}
