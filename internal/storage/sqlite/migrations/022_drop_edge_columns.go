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

	// SQLite 3.35.0+ supports DROP COLUMN, but we use table recreation for compatibility
	// This is idempotent - we recreate the table without the deprecated columns

	// CRITICAL: Disable foreign keys to prevent CASCADE deletes when we drop the issues table
	// The dependencies table has FOREIGN KEY (depends_on_id) REFERENCES issues(id) ON DELETE CASCADE
	// Without disabling foreign keys, dropping the issues table would delete all dependencies!
	_, err = db.Exec(`PRAGMA foreign_keys = OFF`)
	if err != nil {
		return fmt.Errorf("failed to disable foreign keys: %w", err)
	}
	// Re-enable foreign keys at the end (deferred to ensure it runs)
	defer func() {
		_, _ = db.Exec(`PRAGMA foreign_keys = ON`)
	}()

	// Drop views that depend on the issues table BEFORE starting transaction
	// This is necessary because SQLite validates views during table operations
	_, err = db.Exec(`DROP VIEW IF EXISTS ready_issues`)
	if err != nil {
		return fmt.Errorf("failed to drop ready_issues view: %w", err)
	}
	_, err = db.Exec(`DROP VIEW IF EXISTS blocked_issues`)
	if err != nil {
		return fmt.Errorf("failed to drop blocked_issues view: %w", err)
	}

	// Start a transaction for atomicity
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Create new table without the deprecated columns
	_, err = tx.Exec(`
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
			close_reason TEXT DEFAULT '',
			CHECK ((status = 'closed') = (closed_at IS NOT NULL))
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create new issues table: %w", err)
	}

	// Copy data from old table to new table (excluding deprecated columns)
	_, err = tx.Exec(`
		INSERT INTO issues_new (
			id, content_hash, title, description, design, acceptance_criteria,
			notes, status, priority, issue_type, assignee, estimated_minutes,
			created_at, updated_at, closed_at, external_ref, source_repo, compaction_level,
			compacted_at, compacted_at_commit, original_size, deleted_at,
			deleted_by, delete_reason, original_type, sender, ephemeral, close_reason
		)
		SELECT
			id, content_hash, title, description, design, acceptance_criteria,
			notes, status, priority, issue_type, assignee, estimated_minutes,
			created_at, updated_at, closed_at, external_ref, COALESCE(source_repo, ''), compaction_level,
			compacted_at, compacted_at_commit, original_size, deleted_at,
			deleted_by, delete_reason, original_type, sender, ephemeral,
			COALESCE(close_reason, '')
		FROM issues
	`)
	if err != nil {
		return fmt.Errorf("failed to copy issues data: %w", err)
	}

	// Drop old table
	_, err = tx.Exec(`DROP TABLE issues`)
	if err != nil {
		return fmt.Errorf("failed to drop old issues table: %w", err)
	}

	// Rename new table to issues
	_, err = tx.Exec(`ALTER TABLE issues_new RENAME TO issues`)
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
		_, err = tx.Exec(idx)
		if err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit migration: %w", err)
	}

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
