package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateAddNoHistoryColumn adds the no_history column to the issues and wisps tables.
// no_history marks beads stored in the wisps table that should NOT be GC-eligible
// (as opposed to ephemeral wisps which are GC-eligible). Part of gh-2619.
//
// Idempotent: checks for column existence before ALTER.
func MigrateAddNoHistoryColumn(db *sql.DB) error {
	for _, table := range []string{"issues", "wisps"} {
		exists, err := columnExists(db, table, "no_history")
		if err != nil {
			return fmt.Errorf("failed to check no_history column on %s: %w", table, err)
		}
		if exists {
			continue
		}

		//nolint:gosec // G201: table is from hardcoded list
		_, err = db.Exec(fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN no_history TINYINT(1) DEFAULT 0", table))
		if err != nil {
			return fmt.Errorf("failed to add no_history column to %s: %w", table, err)
		}
	}

	return nil
}
