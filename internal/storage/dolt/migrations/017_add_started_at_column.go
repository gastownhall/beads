package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateAddStartedAtColumn ensures the started_at column exists on the
// issues and wisps tables. This is a defensive, idempotent compat migration
// that repairs databases where the embedded SQL migration 0027 was recorded
// as applied but its ALTER TABLE issues statement did not actually run.
//
// Observed on upgrades from v0.63.x (Go-based migrations, no schema_migrations
// tracking) to v1.0.x: the backfillMigrations path runs every embedded
// migration with tolerateExisting=true, swallowing all errors. For the
// two-statement migration 0027, the ALTER on wisps races with
// EnsureIgnoredTables (which has already added started_at to the dolt-ignored
// wisps table) and errors with "duplicate column"; depending on driver
// multi-statement semantics this could mask the preceding ALTER on issues.
// The version was recorded regardless, so subsequent `bd list` queries failed
// with "column started_at could not be found". (GH#3363)
//
// Safe to run unconditionally: each ALTER is gated by a SHOW COLUMNS check.
func MigrateAddStartedAtColumn(db *sql.DB) error {
	for _, table := range []string{"issues", "wisps"} {
		tableOK, err := TableExists(db, table)
		if err != nil {
			return fmt.Errorf("failed to check %s table existence: %w", table, err)
		}
		if !tableOK {
			// wisps is a dolt-ignored table recreated by EnsureIgnoredTables;
			// if it's missing here we skip — the ignored-table path owns it.
			continue
		}

		exists, err := columnExists(db, table, "started_at")
		if err != nil {
			return fmt.Errorf("failed to check started_at column on %s: %w", table, err)
		}
		if exists {
			continue
		}

		//nolint:gosec // G201: table is from hardcoded list
		if _, err := db.Exec(fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN started_at DATETIME", table)); err != nil {
			return fmt.Errorf("failed to add started_at column to %s: %w", table, err)
		}
	}
	return nil
}
