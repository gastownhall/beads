package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// execer is an interface for types that can execute SQL queries
// Both *sql.DB and *sql.Tx implement this interface
type execer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// rebuildBlockedCache completely rebuilds the blocked_issues_cache table
// This is used during cache invalidation when dependencies change
func (s *SQLiteStorage) rebuildBlockedCache(ctx context.Context, tx *sql.Tx) error {
	// Use the transaction if provided, otherwise use direct db connection
	var exec execer = s.db
	if tx != nil {
		exec = tx
	}

	// Clear the cache
	if _, err := exec.ExecContext(ctx, "DELETE FROM blocked_issues_cache"); err != nil {
		return fmt.Errorf("failed to clear blocked_issues_cache: %w", err)
	}

	// Rebuild using the recursive CTE logic
	query := `
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
	`

	if _, err := exec.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("failed to rebuild blocked_issues_cache: %w", err)
	}

	return nil
}

// invalidateBlockedCache rebuilds the blocked issues cache
// Called when dependencies change or issue status changes
func (s *SQLiteStorage) invalidateBlockedCache(ctx context.Context, tx *sql.Tx) error {
	return s.rebuildBlockedCache(ctx, tx)
}
