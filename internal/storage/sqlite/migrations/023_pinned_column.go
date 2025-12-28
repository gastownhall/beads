package migrations

import (
	"database/sql"
	"fmt"
)

// MigratePinnedColumn adds the pinned column to the issues table.
// Pinned issues are persistent context markers that should not be treated as work items.
func MigratePinnedColumn(db *sql.DB) error {
	// Check if column already exists
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'pinned'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check pinned column: %w", err)
	}

	if columnExists {
		// Column exists (e.g. created by new schema); ensure index exists.
		_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_pinned ON issues(pinned) WHERE pinned = 1`)
		if err != nil {
			return fmt.Errorf("failed to create pinned index: %w", err)
		}
		return nil
	}

	// Add the pinned column
	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN pinned INTEGER DEFAULT 0`)
	if err != nil {
		return fmt.Errorf("failed to add pinned column: %w", err)
	}

	// Add index for pinned issues (for efficient filtering)
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_pinned ON issues(pinned) WHERE pinned = 1`)
	if err != nil {
		return fmt.Errorf("failed to create pinned index: %w", err)
	}

	return nil
}
