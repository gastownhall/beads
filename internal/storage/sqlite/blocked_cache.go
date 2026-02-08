// Package sqlite provides the blocked_issues_cache optimization for GetReadyWork performance.
//
// # Performance Impact
//
// GetReadyWork originally used a recursive CTE to compute blocked issues on every query,
// taking ~752ms on a 10K issue database. With the cache, queries complete in ~29ms
// (25x speedup) by using a simple NOT EXISTS check against the materialized cache table.
//
// # Cache Architecture
//
// The blocked_issues_cache table stores issue_id values for all issues that are currently
// blocked. An issue is blocked if:
//   - It has a 'blocks' dependency on an open/in_progress/blocked issue (direct blocking)
//   - It has a 'blocks' dependency on an external:* reference (cross-project blocking, bd-om4a)
//   - It has a 'conditional-blocks' dependency where the blocker hasn't failed (bd-kzda)
//   - It has a 'waits-for' dependency on a spawner with unclosed children (bd-xo1o.2)
//   - Its parent is blocked and it's connected via 'parent-child' dependency (transitive blocking)
//
// WaitsFor gates (bd-xo1o.2): B waits for spawner A's dynamically-bonded children.
// Gate types: "all-children" (default, blocked until ALL close) or "any-children" (until ANY closes).
//
// Conditional blocks (bd-kzda): B runs only if A fails. B is blocked until A is closed
// with a failure close reason (failed, rejected, wontfix, canceled, abandoned, etc.).
// If A succeeds (closed without failure), B stays blocked.
//
// The cache is maintained automatically by invalidating and rebuilding whenever:
//   - A 'blocks', 'conditional-blocks', 'waits-for', or 'parent-child' dependency is added or removed
//   - Any issue's status changes (affects whether it blocks others)
//   - An issue is closed (closed issues don't block others; conditional-blocks checks close_reason)
//
// Related and discovered-from dependencies do NOT trigger cache invalidation since they
// don't affect blocking semantics.
//
// # Cache Invalidation Strategy
//
// On any triggering change, the entire cache is rebuilt from scratch (DELETE + INSERT).
// This full-rebuild approach is chosen because:
//   - Rebuild is fast (<50ms even on 10K databases) due to optimized CTE logic
//   - Simpler implementation than incremental updates
//   - Dependency changes are rare compared to reads
//   - Guarantees consistency - no risk of partial/stale updates
//
// The rebuild happens within the same transaction as the triggering change, ensuring
// atomicity and consistency. The cache can never be in an inconsistent state visible
// to queries.
//
// # Transaction Safety
//
// All cache operations support both transaction and direct database execution:
//   - rebuildBlockedCache accepts optional *sql.Tx parameter
//   - If tx != nil, uses transaction; otherwise uses direct db connection
//   - Cache invalidation during CreateIssue/UpdateIssue/AddDependency happens in their tx
//   - Ensures cache is always consistent with the database state
//
// # Performance Characteristics
//
// Query performance (GetReadyWork):
//   - Before cache: ~752ms (recursive CTE on 10K issues)
//   - With cache: ~29ms (NOT EXISTS check)
//   - Speedup: 25x
//
// Write overhead:
//   - Cache rebuild: <50ms (full DELETE + INSERT)
//   - Only triggered on dependency/status changes (rare operations)
//   - Trade-off: slower writes for much faster reads
//
// # Edge Cases Handled
//
// 1. Parent-child transitive blocking:
//   - Children of blocked parents are automatically marked as blocked
//   - Propagates through arbitrary depth hierarchies (limited to depth 50)
//
// 2. Multiple blockers:
//   - Issue blocked by multiple open issues stays blocked until all are closed
//   - DISTINCT in CTE ensures issue appears once in cache
//
// 3. Status changes:
//   - Closing a blocker removes all blocked descendants from cache
//   - Reopening a blocker adds them back
//
// 4. Dependency removal:
//   - Removing last blocker unblocks the issue
//   - Removing parent-child link unblocks orphaned subtree
//
// 5. Foreign key cascades:
//   - Cache entries automatically deleted when issue is deleted (ON DELETE CASCADE)
//   - No manual cleanup needed
//
// # Future Optimizations
//
// If rebuild becomes a bottleneck in very large databases (>100K issues):
//   - Consider incremental updates for specific dependency types
//   - Add indexes to dependencies table for CTE performance
//   - Implement dirty tracking to avoid rebuilds when cache is unchanged
//
// However, current performance is excellent for realistic workloads.
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
func (s *SQLiteStorage) rebuildBlockedCache(ctx context.Context, exec execer) error {
	// Use direct db connection if no execer provided
	if exec == nil {
		exec = s.db
	}

	// Clear the cache
	if _, err := exec.ExecContext(ctx, "DELETE FROM blocked_issues_cache"); err != nil {
		return fmt.Errorf("failed to clear blocked_issues_cache: %w", err)
	}

	// Rebuild using the recursive CTE logic
	// Only includes local blockers (open issues) - external refs are resolved
	// lazily at query time by GetReadyWork (bd-zmmy supersedes bd-om4a)
	//
	// Handles four blocking types:
	// - 'blocks': B is blocked until A is closed (any close reason)
	// - 'conditional-blocks': B is blocked until A is closed with failure (bd-kzda)
	// - 'waits-for': B is blocked until all children of spawner A are closed (bd-xo1o.2)
	// - 'parent-child': Propagates blockage to children
	//
	// Failure close reasons are detected by matching keywords in close_reason:
	// failed, rejected, wontfix, won't fix, canceled, abandoned,
	// blocked, error, timeout, aborted
	//nolint:misspell // SQL contains both "cancelled" and "canceled" for British/US spelling
	query := `
		INSERT INTO blocked_issues_cache (issue_id)
		WITH RECURSIVE
		  -- Step 1: Find issues blocked directly by LOCAL dependencies
		  -- External refs (external:*) are excluded - they're resolved lazily by GetReadyWork
		  blocked_directly AS (
		    -- Regular 'blocks' dependencies: B blocked if A not closed
		    SELECT DISTINCT d.issue_id
		    FROM dependencies d
		    JOIN issues blocker ON d.depends_on_id = blocker.id
		    WHERE d.type = 'blocks'
		      AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')

		    UNION

		    -- 'conditional-blocks' dependencies: B blocked unless A closed with failure (bd-kzda)
		    -- B is blocked if:
		    --   - A is not closed (still in progress), OR
		    --   - A is closed without a failure indication
		    SELECT DISTINCT d.issue_id
		    FROM dependencies d
		    JOIN issues blocker ON d.depends_on_id = blocker.id
		    WHERE d.type = 'conditional-blocks'
		      AND (
		        -- A is not closed: B stays blocked
		        blocker.status IN ('open', 'in_progress', 'blocked', 'deferred')
		        OR
		        -- A is closed but NOT with a failure: B stays blocked (condition not met)
		        (blocker.status = 'closed' AND NOT (
		          LOWER(COALESCE(blocker.close_reason, '')) LIKE '%failed%'
		          OR LOWER(COALESCE(blocker.close_reason, '')) LIKE '%rejected%'
		          OR LOWER(COALESCE(blocker.close_reason, '')) LIKE '%wontfix%'
		          OR LOWER(COALESCE(blocker.close_reason, '')) LIKE '%won''t fix%'
		          OR LOWER(COALESCE(blocker.close_reason, '')) LIKE '%cancelled%'
		          OR LOWER(COALESCE(blocker.close_reason, '')) LIKE '%canceled%'
		          OR LOWER(COALESCE(blocker.close_reason, '')) LIKE '%abandoned%'
		          OR LOWER(COALESCE(blocker.close_reason, '')) LIKE '%blocked%'
		          OR LOWER(COALESCE(blocker.close_reason, '')) LIKE '%error%'
		          OR LOWER(COALESCE(blocker.close_reason, '')) LIKE '%timeout%'
		          OR LOWER(COALESCE(blocker.close_reason, '')) LIKE '%aborted%'
		        ))
		      )

		    UNION

		    -- 'waits-for' dependencies: B blocked until all children of spawner closed (bd-xo1o.2)
		    -- This is a fanout gate for dynamic molecule bonding
		    -- B waits for A (spawner), blocked while ANY child of A is not closed
		    -- Gate type from metadata: "all-children" (default) or "any-children"
		    SELECT DISTINCT d.issue_id
		    FROM dependencies d
		    WHERE d.type = 'waits-for'
		      AND (
		        -- Default gate: "all-children" - blocked while ANY child is open
		        COALESCE(json_extract(d.metadata, '$.gate'), 'all-children') = 'all-children'
		        AND EXISTS (
		          SELECT 1 FROM dependencies child_dep
		          JOIN issues child ON child_dep.issue_id = child.id
		          WHERE child_dep.type = 'parent-child'
		            AND child_dep.depends_on_id = COALESCE(
		              json_extract(d.metadata, '$.spawner_id'),
		              d.depends_on_id
		            )
		            AND child.status NOT IN ('closed', 'tombstone')
		        )
		        OR
		        -- Alternative gate: "any-children" - blocked until ANY child closes
		        COALESCE(json_extract(d.metadata, '$.gate'), 'all-children') = 'any-children'
		        AND NOT EXISTS (
		          SELECT 1 FROM dependencies child_dep
		          JOIN issues child ON child_dep.issue_id = child.id
		          WHERE child_dep.type = 'parent-child'
		            AND child_dep.depends_on_id = COALESCE(
		              json_extract(d.metadata, '$.spawner_id'),
		              d.depends_on_id
		            )
		            AND child.status IN ('closed', 'tombstone')
		        )
		      )
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
func (s *SQLiteStorage) invalidateBlockedCache(ctx context.Context, exec execer) error {
	return s.rebuildBlockedCache(ctx, exec)
}

// GetBlockedIssueIDs returns all issue IDs currently in the blocked cache
func (s *SQLiteStorage) GetBlockedIssueIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT issue_id FROM blocked_issues_cache")
	if err != nil {
		return nil, fmt.Errorf("failed to query blocked_issues_cache: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan blocked issue ID: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
