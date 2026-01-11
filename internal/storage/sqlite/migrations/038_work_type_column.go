package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateWorkTypeColumn adds work_type column to the issues table.
// This field distinguishes work assignment models per Decision 006.
// Values: 'mutex' (one worker, exclusive - default) or 'open_competition' (many submit, buyer picks)
func MigrateWorkTypeColumn(db *sql.DB) error {
	// Check if column already exists
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'work_type'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check work_type column: %w", err)
	}

	if columnExists {
		return nil
	}

	// Add the column with default 'mutex'
	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN work_type TEXT DEFAULT 'mutex'`)
	if err != nil {
		return fmt.Errorf("failed to add work_type column: %w", err)
	}

	return nil
}
