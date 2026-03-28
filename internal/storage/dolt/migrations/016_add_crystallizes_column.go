package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateAddCrystallizesColumn adds the crystallizes column to the issues and wisps tables.
// crystallizes marks work that compounds over time (code, features) vs work that evaporates
// (ops, support tasks), affecting CV weighting per HOP Decision 006.
//
// This migration is needed for existing databases that were created before the crystallizes
// column was added to the schema. Without this, bd INSERTs fail with:
// 'column crystallizes could not be found in any table in scope' (gt-z6c4m).
//
// Idempotent: checks for column existence before ALTER.
func MigrateAddCrystallizesColumn(db *sql.DB) error {
	for _, table := range []string{"issues", "wisps"} {
		exists, err := columnExists(db, table, "crystallizes")
		if err != nil {
			return fmt.Errorf("failed to check crystallizes column on %s: %w", table, err)
		}
		if exists {
			continue
		}

		//nolint:gosec // G201: table is from hardcoded list
		_, err = db.Exec(fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN crystallizes TINYINT(1) DEFAULT 0", table))
		if err != nil {
			return fmt.Errorf("failed to add crystallizes column to %s: %w", table, err)
		}
	}

	return nil
}
