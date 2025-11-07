package migrations

import (
	"database/sql"
	"fmt"
)

func MigrateSnapshotsTable(db *sql.DB) error {
	var tableExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM sqlite_master
		WHERE type='table' AND name='issue_snapshots'
	`).Scan(&tableExists)
	if err != nil {
		return fmt.Errorf("failed to check issue_snapshots table: %w", err)
	}

	if tableExists {
		return nil
	}

	_, err = db.Exec(`
		CREATE TABLE issue_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id TEXT NOT NULL,
			snapshot_time DATETIME NOT NULL,
			compaction_level INTEGER NOT NULL,
			original_size INTEGER NOT NULL,
			compressed_size INTEGER NOT NULL,
			original_content TEXT NOT NULL,
			archived_events TEXT,
			FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
		);
		CREATE INDEX idx_snapshots_issue ON issue_snapshots(issue_id);
		CREATE INDEX idx_snapshots_level ON issue_snapshots(compaction_level);
	`)
	if err != nil {
		return fmt.Errorf("failed to create issue_snapshots table: %w", err)
	}

	return nil
}
