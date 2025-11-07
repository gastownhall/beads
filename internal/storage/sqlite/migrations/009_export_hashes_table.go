package migrations

import (
	"database/sql"
	"fmt"
)

func MigrateExportHashesTable(db *sql.DB) error {
	var tableName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='export_hashes'
	`).Scan(&tableName)

	if err == sql.ErrNoRows {
		_, err := db.Exec(`
			CREATE TABLE export_hashes (
				issue_id TEXT PRIMARY KEY,
				content_hash TEXT NOT NULL,
				exported_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
			)
		`)
		if err != nil {
			return fmt.Errorf("failed to create export_hashes table: %w", err)
		}
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check export_hashes table: %w", err)
	}

	return nil
}
