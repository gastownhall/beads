package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateCrystallizesColumn adds the crystallizes column to the issues table.
// Crystallizes tracks whether work compounds over time (true: code, features)
// or evaporates (false: ops, support). Per Decision 006, this affects CV weighting.
// Default is false (conservative - work evaporates unless explicitly marked).
func MigrateCrystallizesColumn(db *sql.DB) error {
	// Check if column already exists
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'crystallizes'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check crystallizes column: %w", err)
	}

	if columnExists {
		// Column already exists (e.g. created by new schema)
		return nil
	}

	// Add the crystallizes column
	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN crystallizes INTEGER DEFAULT 0`)
	if err != nil {
		return fmt.Errorf("failed to add crystallizes column: %w", err)
	}

	return nil
}
