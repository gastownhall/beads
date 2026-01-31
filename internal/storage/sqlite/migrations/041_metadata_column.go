package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateMetadataColumn adds the metadata column to the issues table.
// This stores arbitrary JSON data for extension points (tool annotations, file lists, etc.).
// See GH#1406 for the feature request.
func MigrateMetadataColumn(db *sql.DB) error {
	// Check if column already exists
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'metadata'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check metadata column: %w", err)
	}

	if columnExists {
		return nil
	}

	// Add the metadata column (TEXT, NOT NULL, default empty JSON object)
	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`)
	if err != nil {
		return fmt.Errorf("failed to add metadata column: %w", err)
	}

	return nil
}
