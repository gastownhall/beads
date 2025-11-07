package migrations

import (
	"database/sql"
	"fmt"
)

func MigrateCompactedAtCommitColumn(db *sql.DB) error {
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'compacted_at_commit'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check compacted_at_commit column: %w", err)
	}

	if columnExists {
		return nil
	}

	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN compacted_at_commit TEXT`)
	if err != nil {
		return fmt.Errorf("failed to add compacted_at_commit column: %w", err)
	}

	return nil
}
