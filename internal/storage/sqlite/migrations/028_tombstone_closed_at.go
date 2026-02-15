package migrations

import (
	"database/sql"
	"fmt"
	"strings"
)

// MigrateTombstoneClosedAt is a legacy migration that relaxes the closed_at constraint.
// The soft-delete status is no longer used (Dolt handles delete propagation natively)
// but this migration must remain for existing databases that already have it recorded.
// It recreates the issues table with a relaxed CHECK constraint.
func MigrateTombstoneClosedAt(db *sql.DB) error {
	// SQLite doesn't support ALTER TABLE to modify CHECK constraints
	// We must recreate the table with the new constraint

	// Idempotency check: see if the new CHECK constraint already exists
	// The relaxed constraint contains a legacy status clause that the old one didn't
	var tableSql string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='issues'`).Scan(&tableSql)
	if err != nil {
		return fmt.Errorf("failed to get issues table schema: %w", err)
	}
	// If the schema already has the relaxed constraint clause, migration is already applied
	if strings.Contains(tableSql, "status = 'tombstone'") || strings.Contains(tableSql, `status = "tombstone"`) {
		return nil
	}

	// Step 0: Drop views that depend on the issues table
	_, err = db.Exec(`DROP VIEW IF EXISTS ready_issues`)
	if err != nil {
		return fmt.Errorf("failed to drop ready_issues view: %w", err)
	}
	_, err = db.Exec(`DROP VIEW IF EXISTS blocked_issues`)
	if err != nil {
		return fmt.Errorf("failed to drop blocked_issues view: %w", err)
	}

	// Step 1: Create new table with updated constraint
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
			created_by TEXT DEFAULT '',
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
			pinned INTEGER DEFAULT 0,
			is_template INTEGER DEFAULT 0,
			await_type TEXT,
			await_id TEXT,
			timeout_ns INTEGER,
			waiters TEXT,
			CHECK (
				(status = 'closed' AND closed_at IS NOT NULL) OR
				(status = 'tombstone') OR
				(status NOT IN ('closed', 'tombstone') AND closed_at IS NULL)
			)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create new issues table: %w", err)
	}

	// Step 2: Copy data from old table to new table
	// We need to check if created_by column exists in the old table
	// If not, we insert a default empty string for it
	var hasCreatedBy bool
	rows, err := db.Query(`PRAGMA table_info(issues)`)
	if err != nil {
		return fmt.Errorf("failed to get table info: %w", err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("failed to scan table info: %w", err)
		}
		if name == "created_by" {
			hasCreatedBy = true
			break
		}
	}
	_ = rows.Close()

	var insertSQL string
	if hasCreatedBy {
		// Old table has created_by, copy all columns directly
		insertSQL = `
			INSERT INTO issues_new (
				id, content_hash, title, description, design, acceptance_criteria, notes,
				status, priority, issue_type, assignee, estimated_minutes, created_at,
				created_by, updated_at, closed_at, external_ref, source_repo, compaction_level,
				compacted_at, compacted_at_commit, original_size, deleted_at, deleted_by,
				delete_reason, original_type, sender, ephemeral, close_reason, pinned,
				is_template, await_type, await_id, timeout_ns, waiters
			)
			SELECT
				id, content_hash, title, description, design, acceptance_criteria, notes,
				status, priority, issue_type, assignee, estimated_minutes, created_at,
				created_by, updated_at, closed_at, external_ref, source_repo, compaction_level,
				compacted_at, compacted_at_commit, original_size, deleted_at, deleted_by,
				delete_reason, original_type, sender, ephemeral, close_reason, pinned,
				is_template, await_type, await_id, timeout_ns, waiters
			FROM issues
		`
	} else {
		// Old table doesn't have created_by, use empty string default
		insertSQL = `
			INSERT INTO issues_new (
				id, content_hash, title, description, design, acceptance_criteria, notes,
				status, priority, issue_type, assignee, estimated_minutes, created_at,
				created_by, updated_at, closed_at, external_ref, source_repo, compaction_level,
				compacted_at, compacted_at_commit, original_size, deleted_at, deleted_by,
				delete_reason, original_type, sender, ephemeral, close_reason, pinned,
				is_template, await_type, await_id, timeout_ns, waiters
			)
			SELECT
				id, content_hash, title, description, design, acceptance_criteria, notes,
				status, priority, issue_type, assignee, estimated_minutes, created_at,
				'', updated_at, closed_at, external_ref, source_repo, compaction_level,
				compacted_at, compacted_at_commit, original_size, deleted_at, deleted_by,
				delete_reason, original_type, sender, ephemeral, close_reason, pinned,
				is_template, await_type, await_id, timeout_ns, waiters
			FROM issues
		`
	}

	_, err = db.Exec(insertSQL)
	if err != nil {
		return fmt.Errorf("failed to copy issues data: %w", err)
	}

	// Step 3: Drop old table
	_, err = db.Exec(`DROP TABLE issues`)
	if err != nil {
		return fmt.Errorf("failed to drop old issues table: %w", err)
	}

	// Step 4: Rename new table to original name
	_, err = db.Exec(`ALTER TABLE issues_new RENAME TO issues`)
	if err != nil {
		return fmt.Errorf("failed to rename new issues table: %w", err)
	}

	// Step 5: Recreate indexes (they were dropped with the table)
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_issues_status ON issues(status)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_priority ON issues(priority)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_assignee ON issues(assignee)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_created_at ON issues(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_external_ref ON issues(external_ref) WHERE external_ref IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_issues_pinned ON issues(pinned) WHERE pinned = 1`,
		`CREATE INDEX IF NOT EXISTS idx_issues_is_template ON issues(is_template) WHERE is_template = 1`,
		`CREATE INDEX IF NOT EXISTS idx_issues_updated_at ON issues(updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_status_priority ON issues(status, priority)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_gate ON issues(issue_type) WHERE issue_type = 'gate'`,
	}

	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}

	// Step 6: Recreate views that we dropped
	_, err = db.Exec(`
		CREATE VIEW IF NOT EXISTS ready_issues AS
		WITH RECURSIVE
		  blocked_directly AS (
		    SELECT DISTINCT d.issue_id
		    FROM dependencies d
		    JOIN issues blocker ON d.depends_on_id = blocker.id
		    WHERE d.type = 'blocks'
		      AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred')
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

	_, err = db.Exec(`
		CREATE VIEW IF NOT EXISTS blocked_issues AS
		SELECT
		    i.*,
		    COUNT(d.depends_on_id) as blocked_by_count
		FROM issues i
		JOIN dependencies d ON i.id = d.issue_id
		JOIN issues blocker ON d.depends_on_id = blocker.id
		WHERE i.status IN ('open', 'in_progress', 'blocked', 'deferred')
		  AND d.type = 'blocks'
		  AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred')
		GROUP BY i.id
	`)
	if err != nil {
		return fmt.Errorf("failed to recreate blocked_issues view: %w", err)
	}

	return nil
}
