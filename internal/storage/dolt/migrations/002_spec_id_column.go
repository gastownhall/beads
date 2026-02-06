package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateSpecIDColumn adds the spec_id column to the issues table.
// This stores a path or identifier to an external specification document.
// New databases already have this column from the schema definition;
// this migration handles databases created before it was added.
func MigrateSpecIDColumn(db *sql.DB) error {
	exists, err := columnExists(db, "issues", "spec_id")
	if err != nil {
		return fmt.Errorf("failed to check spec_id column: %w", err)
	}
	if exists {
		return nil
	}

	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN spec_id VARCHAR(1024)`)
	if err != nil {
		return fmt.Errorf("failed to add spec_id column: %w", err)
	}

	// Add index for spec_id lookups
	_, err = db.Exec(`CREATE INDEX idx_issues_spec_id ON issues(spec_id)`)
	if err != nil {
		return fmt.Errorf("failed to create spec_id index: %w", err)
	}

	return nil
}
