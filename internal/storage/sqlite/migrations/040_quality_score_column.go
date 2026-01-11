package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateQualityScoreColumn adds the quality_score column to the issues table.
// This stores an aggregate quality score (0.0-1.0) set by Refineries on merge.
// NULL indicates no score has been assigned yet.
func MigrateQualityScoreColumn(db *sql.DB) error {
	// Check if column already exists
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'quality_score'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check quality_score column: %w", err)
	}

	if columnExists {
		return nil
	}

	// Add the quality_score column (REAL, nullable - no default)
	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN quality_score REAL`)
	if err != nil {
		return fmt.Errorf("failed to add quality_score column: %w", err)
	}

	return nil
}
