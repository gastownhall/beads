package migrations

import (
	"database/sql"
)

// MigrateRemoveDependsOnFK removes the FOREIGN KEY constraint on depends_on_id
// to allow external dependencies (external:<project>:<capability>).
// See bd-zmmy for design context.
func MigrateRemoveDependsOnFK(db *sql.DB) error {
	// Disable foreign keys for table recreation
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() { _, _ = db.Exec(`PRAGMA foreign_keys = ON`) }()

	// Begin transaction for atomic table recreation
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Drop views that depend on the dependencies table
	// They will be recreated after the table is rebuilt
	if _, err = tx.Exec(`DROP VIEW IF EXISTS ready_issues`); err != nil {
		return err
	}
	if _, err = tx.Exec(`DROP VIEW IF EXISTS blocked_issues`); err != nil {
		return err
	}

	// Create new table without FK on depends_on_id
	// Keep FK on issue_id (source must exist)
	// Remove FK on depends_on_id (target can be external ref)
	if _, err = tx.Exec(`
		CREATE TABLE dependencies_new (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT 'blocks',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			created_by TEXT NOT NULL,
			metadata TEXT,
			thread_id TEXT,
			PRIMARY KEY (issue_id, depends_on_id, type),
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
		)
	`); err != nil {
		return err
	}

	// Copy data from old table
	if _, err = tx.Exec(`
		INSERT INTO dependencies_new
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM dependencies
	`); err != nil {
		return err
	}

	// Drop old table
	if _, err = tx.Exec(`DROP TABLE dependencies`); err != nil {
		return err
	}

	// Rename new table
	if _, err = tx.Exec(`ALTER TABLE dependencies_new RENAME TO dependencies`); err != nil {
		return err
	}

	// Recreate indexes
	if _, err = tx.Exec(`
		CREATE INDEX idx_dependencies_issue_id ON dependencies(issue_id)
	`); err != nil {
		return err
	}

	if _, err = tx.Exec(`
		CREATE INDEX idx_dependencies_depends_on ON dependencies(depends_on_id)
	`); err != nil {
		return err
	}

	if _, err = tx.Exec(`
		CREATE INDEX idx_dependencies_type ON dependencies(type)
	`); err != nil {
		return err
	}

	if _, err = tx.Exec(`
		CREATE INDEX idx_dependencies_depends_on_type ON dependencies(depends_on_id, type)
	`); err != nil {
		return err
	}

	if _, err = tx.Exec(`
		CREATE INDEX idx_dependencies_depends_on_type_issue ON dependencies(depends_on_id, type, issue_id)
	`); err != nil {
		return err
	}

	// Recreate views
	if _, err = tx.Exec(`
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
	`); err != nil {
		return err
	}

	if _, err = tx.Exec(`
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
	`); err != nil {
		return err
	}

	return tx.Commit()
}
