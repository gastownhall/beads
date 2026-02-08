package migrations

import (
	"database/sql"
	"fmt"
)

func MigrateDirtyIssuesTable(db *sql.DB) error {
	var tableName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='dirty_issues'
	`).Scan(&tableName)

	if err == sql.ErrNoRows {
		_, err := db.Exec(`
			CREATE TABLE dirty_issues (
				issue_id TEXT PRIMARY KEY,
				marked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
			);
			CREATE INDEX idx_dirty_issues_marked_at ON dirty_issues(marked_at);
		`)
		if err != nil {
			return fmt.Errorf("failed to create dirty_issues table: %w", err)
		}
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check for dirty_issues table: %w", err)
	}

	var hasContentHash bool
	err = db.QueryRow(`
		SELECT COUNT(*) > 0 FROM pragma_table_info('dirty_issues')
		WHERE name = 'content_hash'
	`).Scan(&hasContentHash)

	if err != nil {
		return fmt.Errorf("failed to check for content_hash column: %w", err)
	}

	if !hasContentHash {
		_, err = db.Exec(`ALTER TABLE dirty_issues ADD COLUMN content_hash TEXT`)
		if err != nil {
			return fmt.Errorf("failed to add content_hash column: %w", err)
		}
	}

	return nil
}
