package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateSourceSystemColumn adds the source_system column to the issues table.
// This tracks which adapter/system created the issue for federation support.
func MigrateSourceSystemColumn(db *sql.DB) error {
	// Check if column already exists
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'source_system'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check source_system column: %w", err)
	}

	if columnExists {
		return nil
	}

	// Add the source_system column
	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN source_system TEXT DEFAULT ''`)
	if err != nil {
		return fmt.Errorf("failed to add source_system column: %w", err)
	}

	return nil
}
