package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateDropHOPColumns removes the HOP-specific quality_score and crystallizes
// columns from the issues and wisps tables. These were Gas Town agent credibility
// fields that don't belong in the beads schema.
func MigrateDropHOPColumns(db *sql.DB) error {
	for _, table := range []string{"issues", "wisps"} {
		for _, col := range []string{"quality_score", "crystallizes"} {
			exists, err := columnExists(db, table, col)
			if err != nil {
				return fmt.Errorf("check %s.%s: %w", table, col, err)
			}
			if !exists {
				continue
			}
			//nolint:gosec // G202: table/col are internal constants
			_, err = db.Exec(fmt.Sprintf("ALTER TABLE `%s` DROP COLUMN `%s`", table, col))
			if err != nil {
				return fmt.Errorf("drop %s.%s: %w", table, col, err)
			}
		}
	}
	return nil
}
