package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateIsTemplateColumn adds the is_template column to the issues table.
// Template issues (molecules) are read-only templates that should be filtered
// from work views by default (beads-1ra).
func MigrateIsTemplateColumn(db *sql.DB) error {
	// Check if column already exists
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'is_template'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check is_template column: %w", err)
	}

	if columnExists {
		// Column exists (e.g. created by new schema); ensure index exists.
		_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_is_template ON issues(is_template) WHERE is_template = 1`)
		if err != nil {
			return fmt.Errorf("failed to create is_template index: %w", err)
		}
		return nil
	}

	// Add the is_template column
	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN is_template INTEGER DEFAULT 0`)
	if err != nil {
		return fmt.Errorf("failed to add is_template column: %w", err)
	}

	// Add index for template issues (for efficient filtering)
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_is_template ON issues(is_template) WHERE is_template = 1`)
	if err != nil {
		return fmt.Errorf("failed to create is_template index: %w", err)
	}

	return nil
}
