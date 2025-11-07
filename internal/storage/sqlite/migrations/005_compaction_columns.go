package migrations

import (
	"database/sql"
	"fmt"
)

func MigrateCompactionColumns(db *sql.DB) error {
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'compaction_level'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check compaction_level column: %w", err)
	}

	if columnExists {
		return nil
	}

	_, err = db.Exec(`
		ALTER TABLE issues ADD COLUMN compaction_level INTEGER DEFAULT 0;
		ALTER TABLE issues ADD COLUMN compacted_at DATETIME;
		ALTER TABLE issues ADD COLUMN original_size INTEGER;
	`)
	if err != nil {
		return fmt.Errorf("failed to add compaction columns: %w", err)
	}

	return nil
}
