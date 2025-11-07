package migrations

import (
	"database/sql"
	"fmt"
)

func MigrateRepoMtimesTable(db *sql.DB) error {
	var tableName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='repo_mtimes'
	`).Scan(&tableName)

	if err == sql.ErrNoRows {
		_, err := db.Exec(`
			CREATE TABLE repo_mtimes (
				repo_path TEXT PRIMARY KEY,
				jsonl_path TEXT NOT NULL,
				mtime_ns INTEGER NOT NULL,
				last_checked DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			);
			CREATE INDEX idx_repo_mtimes_checked ON repo_mtimes(last_checked);
		`)
		if err != nil {
			return fmt.Errorf("failed to create repo_mtimes table: %w", err)
		}
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check for repo_mtimes table: %w", err)
	}

	return nil
}
