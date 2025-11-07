package migrations

import (
	"database/sql"
	"fmt"
)

func MigrateChildCountersTable(db *sql.DB) error {
	var tableName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='child_counters'
	`).Scan(&tableName)

	if err == sql.ErrNoRows {
		_, err := db.Exec(`
			CREATE TABLE child_counters (
				parent_id TEXT PRIMARY KEY,
				last_child INTEGER NOT NULL DEFAULT 0,
				FOREIGN KEY (parent_id) REFERENCES issues(id) ON DELETE CASCADE
			)
		`)
		if err != nil {
			return fmt.Errorf("failed to create child_counters table: %w", err)
		}
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check for child_counters table: %w", err)
	}

	return nil
}
