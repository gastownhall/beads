package migrations

import (
	"database/sql"
	"fmt"
)

// MigratePinnedColumn adds the pinned column to the issues table.
// Pinned issues are visually marked and can be filtered with --pinned/--no-pinned flags.
func MigratePinnedColumn(db *sql.DB) error {
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
		return nil
	}

	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN pinned INTEGER DEFAULT 0`)
	if err != nil {
		return fmt.Errorf("failed to add pinned column: %w", err)
	}

	// Add partial index for efficient pinned issue queries
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_pinned ON issues(pinned) WHERE pinned = 1`)
	if err != nil {
		return fmt.Errorf("failed to create pinned index: %w", err)
	}

	return nil
}
