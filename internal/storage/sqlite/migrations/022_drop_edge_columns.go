package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateDropEdgeColumns removes the deprecated edge fields from the issues table.
// This is Phase 4 of the Edge Schema Consolidation (Decision 004).
//
// Removes columns:
// - replies_to (now: replies-to dependency)
// - relates_to (now: relates-to dependencies)
// - duplicate_of (now: duplicates dependency)
// - superseded_by (now: supersedes dependency)
//
// Prerequisites:
// - Migration 021 (migrate_edge_fields) must have already run to convert data
// - All code must be updated to use the dependencies API
//
// SQLite doesn't support DROP COLUMN directly in older versions, so we
// recreate the table without the deprecated columns.
func MigrateDropEdgeColumns(db *sql.DB) error {
	// Check if any of the columns still exist
	var hasRepliesTo, hasRelatesTo, hasDuplicateOf, hasSupersededBy bool

	checkCol := func(name string) (bool, error) {
		var exists bool
		err := db.QueryRow(`
			SELECT COUNT(*) > 0
			FROM pragma_table_info('issues')
			WHERE name = ?
		`, name).Scan(&exists)
		return exists, err
	}

	var err error
	hasRepliesTo, err = checkCol("replies_to")
	if err != nil {
		return fmt.Errorf("failed to check replies_to column: %w", err)
	}
	hasRelatesTo, err = checkCol("relates_to")
	if err != nil {
		return fmt.Errorf("failed to check relates_to column: %w", err)
	}
	hasDuplicateOf, err = checkCol("duplicate_of")
	if err != nil {
		return fmt.Errorf("failed to check duplicate_of column: %w", err)
	}
	hasSupersededBy, err = checkCol("superseded_by")
	if err != nil {
		return fmt.Errorf("failed to check superseded_by column: %w", err)
	}

	// If none of the columns exist, migration already ran
	if !hasRepliesTo && !hasRelatesTo && !hasDuplicateOf && !hasSupersededBy {
		return nil
	}

	// Preserve newer columns if they already exist (migration may run on partially-migrated DBs).
	hasPinned, err := checkCol("pinned")
	if err != nil {
		return fmt.Errorf("failed to check pinned column: %w", err)
	}
	hasIsTemplate, err := checkCol("is_template")
	if err != nil {
		return fmt.Errorf("failed to check is_template column: %w", err)
	}
	hasAwaitType, err := checkCol("await_type")
	if err != nil {
		return fmt.Errorf("failed to check await_type column: %w", err)
	}
	hasAwaitID, err := checkCol("await_id")
	if err != nil {
		return fmt.Errorf("failed to check await_id column: %w", err)
	}
	hasTimeoutNs, err := checkCol("timeout_ns")
	if err != nil {
		return fmt.Errorf("failed to check timeout_ns column: %w", err)
	}
	hasWaiters, err := checkCol("waiters")
	if err != nil {
		return fmt.Errorf("failed to check waiters column: %w", err)
	}

	pinnedExpr := "0"
	if hasPinned {
		pinnedExpr = "pinned"
	}
	isTemplateExpr := "0"
	if hasIsTemplate {
		isTemplateExpr = "is_template"
	}
	awaitTypeExpr := "''"
	if hasAwaitType {
		awaitTypeExpr = "await_type"
	}
	awaitIDExpr := "''"
	if hasAwaitID {
		awaitIDExpr = "await_id"
	}
	timeoutNsExpr := "0"
	if hasTimeoutNs {
		timeoutNsExpr = "timeout_ns"
	}
	waitersExpr := "''"
	if hasWaiters {
		waitersExpr = "waiters"
	}

	// SQLite 3.35.0+ supports DROP COLUMN, but we use table recreation for compatibility
	// This is idempotent - we recreate the table without the deprecated columns

	// NOTE: Foreign keys are disabled in RunMigrations() before the EXCLUSIVE transaction starts.
	// This prevents ON DELETE CASCADE from deleting dependencies when we drop/recreate the issues table.

	// Drop views that depend on the issues table BEFORE starting savepoint
	// This is necessary because SQLite validates views during table operations
	_, err = db.Exec(`DROP VIEW IF EXISTS ready_issues`)
	if err != nil {
		return fmt.Errorf("failed to drop ready_issues view: %w", err)
	}
	_, err = db.Exec(`DROP VIEW IF EXISTS blocked_issues`)
	if err != nil {
		return fmt.Errorf("failed to drop blocked_issues view: %w", err)
	}

	// Use SAVEPOINT for atomicity (we're already inside an EXCLUSIVE transaction from RunMigrations)
	// SQLite doesn't support nested transactions but SAVEPOINTs work inside transactions
	_, err = db.Exec(`SAVEPOINT drop_edge_columns`)
	if err != nil {
		return fmt.Errorf("failed to create savepoint: %w", err)
	}
	savepointReleased := false
	defer func() {
		if !savepointReleased {
			_, _ = db.Exec(`ROLLBACK TO SAVEPOINT drop_edge_columns`)
		}
	}()

	// Create new table without the deprecated columns
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS issues_new (
			id TEXT PRIMARY KEY,
			content_hash TEXT,
			title TEXT NOT NULL CHECK(length(title) <= 500),
			description TEXT NOT NULL DEFAULT '',
			design TEXT NOT NULL DEFAULT '',
			acceptance_criteria TEXT NOT NULL DEFAULT '',
			notes TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'open',
			priority INTEGER NOT NULL DEFAULT 2 CHECK(priority >= 0 AND priority <= 4),
			issue_type TEXT NOT NULL DEFAULT 'task',
			assignee TEXT,
			estimated_minutes INTEGER,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			closed_at DATETIME,
			external_ref TEXT,
			source_repo TEXT DEFAULT '',
			compaction_level INTEGER DEFAULT 0,
			compacted_at DATETIME,
			compacted_at_commit TEXT,
			original_size INTEGER,
			deleted_at DATETIME,
			deleted_by TEXT DEFAULT '',
			delete_reason TEXT DEFAULT '',
			original_type TEXT DEFAULT '',
			sender TEXT DEFAULT '',
			ephemeral INTEGER DEFAULT 0,
			pinned INTEGER DEFAULT 0,
			is_template INTEGER DEFAULT 0,
			await_type TEXT,
			await_id TEXT,
			timeout_ns INTEGER,
			waiters TEXT,
			close_reason TEXT DEFAULT '',
			CHECK ((status = 'closed') = (closed_at IS NOT NULL))
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create new issues table: %w", err)
	}

	// Copy data from old table to new table (excluding deprecated columns)
	// NOTE: We use fmt.Sprintf here (not db.Exec parameters) because we're interpolating
	// column names/expressions, not values. db.Exec parameters only work for VALUES.
	// #nosec G201 - expressions are column names, not user input
	copySQL := fmt.Sprintf(`
		INSERT INTO issues_new (
			id, content_hash, title, description, design, acceptance_criteria,
			notes, status, priority, issue_type, assignee, estimated_minutes,
			created_at, updated_at, closed_at, external_ref, source_repo, compaction_level,
			compacted_at, compacted_at_commit, original_size, deleted_at,
			deleted_by, delete_reason, original_type, sender, ephemeral, pinned, is_template,
			await_type, await_id, timeout_ns, waiters, close_reason
		)
		SELECT
			id, content_hash, title, description, design, acceptance_criteria,
			notes, status, priority, issue_type, assignee, estimated_minutes,
			created_at, updated_at, closed_at, external_ref, COALESCE(source_repo, ''), compaction_level,
			compacted_at, compacted_at_commit, original_size, deleted_at,
			deleted_by, delete_reason, original_type, sender, ephemeral,
			%s, %s,
			%s, %s, %s, %s,
			COALESCE(close_reason, '')
		FROM issues
	`, pinnedExpr, isTemplateExpr, awaitTypeExpr, awaitIDExpr, timeoutNsExpr, waitersExpr)
	_, err = db.Exec(copySQL)
	if err != nil {
		return fmt.Errorf("failed to copy issues data: %w", err)
	}

	// Drop old table
	_, err = db.Exec(`DROP TABLE issues`)
	if err != nil {
		return fmt.Errorf("failed to drop old issues table: %w", err)
	}

	// Rename new table to issues
	_, err = db.Exec(`ALTER TABLE issues_new RENAME TO issues`)
	if err != nil {
		return fmt.Errorf("failed to rename new issues table: %w", err)
	}

	// Recreate indexes
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_issues_status ON issues(status)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_priority ON issues(priority)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_assignee ON issues(assignee)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_created_at ON issues(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_external_ref ON issues(external_ref) WHERE external_ref IS NOT NULL`,
	}

	for _, idx := range indexes {
		_, err = db.Exec(idx)
		if err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}

	// Release savepoint (commits the changes within the outer transaction)
	_, err = db.Exec(`RELEASE SAVEPOINT drop_edge_columns`)
	if err != nil {
		return fmt.Errorf("failed to release savepoint: %w", err)
	}
	savepointReleased = true

	// Recreate views that we dropped earlier (after commit, outside transaction)
	// ready_issues view
	_, err = db.Exec(`
		CREATE VIEW IF NOT EXISTS ready_issues AS
		WITH RECURSIVE
		  blocked_directly AS (
		    SELECT DISTINCT d.issue_id
		    FROM dependencies d
		    JOIN issues blocker ON d.depends_on_id = blocker.id
		    WHERE d.type = 'blocks'
		      AND blocker.status IN ('open', 'in_progress', 'blocked')
		  ),
		  blocked_transitively AS (
		    SELECT issue_id, 0 as depth
		    FROM blocked_directly
		    UNION ALL
		    SELECT d.issue_id, bt.depth + 1
		    FROM blocked_transitively bt
		    JOIN dependencies d ON d.depends_on_id = bt.issue_id
		    WHERE d.type = 'parent-child'
		      AND bt.depth < 50
		  )
		SELECT i.*
		FROM issues i
		WHERE i.status = 'open'
		  AND NOT EXISTS (
		    SELECT 1 FROM blocked_transitively WHERE issue_id = i.id
		  )
	`)
	if err != nil {
		return fmt.Errorf("failed to recreate ready_issues view: %w", err)
	}

	// blocked_issues view
	_, err = db.Exec(`
		CREATE VIEW IF NOT EXISTS blocked_issues AS
		SELECT
		    i.*,
		    COUNT(d.depends_on_id) as blocked_by_count
		FROM issues i
		JOIN dependencies d ON i.id = d.issue_id
		JOIN issues blocker ON d.depends_on_id = blocker.id
		WHERE i.status IN ('open', 'in_progress', 'blocked')
		  AND d.type = 'blocks'
		  AND blocker.status IN ('open', 'in_progress', 'blocked')
		GROUP BY i.id
	`)
	if err != nil {
		return fmt.Errorf("failed to recreate blocked_issues view: %w", err)
	}

	return nil
}
