package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateCreatedByColumn adds the created_by column to the issues table.
// This tracks who created the issue, using the same actor chain as comment authors
// (--actor flag, BD_ACTOR env, or $USER). GH#748.
func MigrateCreatedByColumn(db *sql.DB) error {
	// Check if column already exists
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'created_by'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check created_by column: %w", err)
	}

	if columnExists {
		return nil
	}

	// Add the created_by column
	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN created_by TEXT DEFAULT ''`)
	if err != nil {
		return fmt.Errorf("failed to add created_by column: %w", err)
	}

	return nil
}
