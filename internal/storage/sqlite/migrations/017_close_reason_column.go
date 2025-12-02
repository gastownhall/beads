package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateCloseReasonColumn adds the close_reason column to the issues table.
// This column stores the reason provided when closing an issue.
func MigrateCloseReasonColumn(db *sql.DB) error {
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'close_reason'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check close_reason column: %w", err)
	}

	if columnExists {
		return nil
	}

	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN close_reason TEXT DEFAULT ''`)
	if err != nil {
		return fmt.Errorf("failed to add close_reason column: %w", err)
	}

	return nil
}
