package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateDropChildCountersFK removes the fk_counter_parent foreign key from
// child_counters. This FK references issues(id), but since migration 007
// (infra_to_wisps) moved agent beads to the wisps table, set-state on agent
// beads fails with a FK violation — the parent_id exists in wisps, not issues.
//
// child_counters is a sequence counter cache (tracks the highest child ID per
// parent). It is not a domain relationship — orphaned rows are harmless and
// the FK's ON DELETE CASCADE provided no meaningful integrity guarantee.
func MigrateDropChildCountersFK(db *sql.DB) error {
	exists, err := constraintExists(db, "child_counters", "fk_counter_parent")
	if err != nil {
		return fmt.Errorf("checking fk_counter_parent: %w", err)
	}
	if !exists {
		return nil // Already dropped or never existed
	}

	_, err = db.Exec("ALTER TABLE child_counters DROP FOREIGN KEY fk_counter_parent")
	if err != nil {
		return fmt.Errorf("dropping fk_counter_parent: %w", err)
	}

	return nil
}
