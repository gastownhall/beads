package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateSpecIDColumn adds the spec_id column to the issues table.
// This stores a path or identifier to an external specification document.
func MigrateSpecIDColumn(db *sql.DB) error {
	// Check if column already exists
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'spec_id'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check spec_id column: %w", err)
	}

	if !columnExists {
		_, err = db.Exec(`ALTER TABLE issues ADD COLUMN spec_id TEXT`)
		if err != nil {
			return fmt.Errorf("failed to add spec_id column: %w", err)
		}
	}

	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_spec_id ON issues(spec_id)`)
	if err != nil {
		return fmt.Errorf("failed to create spec_id index: %w", err)
	}

	return nil
}
