package schema

import (
	"context"
	"fmt"
)

// Pre-migration repairs run immediately before a specific pending migration
// file is applied. Shipped migration files are frozen (see
// scripts/check-migration-hygiene.sh): editing one forks fresh clones from
// upgraded clones via the recorded content hash, and a bug that makes a
// migration FAIL on drifted databases cannot be fixed forward with a new
// migration either — the failing file aborts the pass before any later
// version runs. Repairing the drift in code, keyed to the pending version,
// is the only path that heals affected databases without touching shipped
// SQL. Precedent: ensureContentHashColumn, the aux row-id backfill.

// preMigrationRepair dispatches any repair registered for (source, version).
func (m migrationSource) preMigrationRepair(ctx context.Context, db DBConn, version int) error {
	if m.cursorTable == "schema_migrations" && version == 53 {
		return ensureIssuesRigColumns(ctx, db)
	}
	return nil
}

// ensureIssuesRigColumns repairs #4502: the rig/agent columns were only ever
// added to the squashed bootstrap 0001_create_issues, so a database
// bootstrapped before they existed reaches schema v52 without them, and
// migration 0053 — which copies exactly these columns from wisps into
// issues — fails with "Unknown column" even with zero rig wisps to repair.
// Databases in the wild may have some but not all six, so each is checked
// individually. Definitions mirror the current bootstrap schema.
func ensureIssuesRigColumns(ctx context.Context, db DBConn) error {
	columns := []struct{ name, definition string }{
		{"hook_bead", "VARCHAR(255) DEFAULT ''"},
		{"role_bead", "VARCHAR(255) DEFAULT ''"},
		{"agent_state", "VARCHAR(32) DEFAULT ''"},
		{"last_activity", "DATETIME"},
		{"role_type", "VARCHAR(32) DEFAULT ''"},
		{"rig", "VARCHAR(255) DEFAULT ''"},
	}
	for _, col := range columns {
		var present int
		if err := db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'issues' AND COLUMN_NAME = ?
		`, col.name).Scan(&present); err != nil {
			return fmt.Errorf("checking issues.%s: %w", col.name, err)
		}
		if present > 0 {
			continue
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE issues ADD COLUMN "+col.name+" "+col.definition); err != nil {
			return fmt.Errorf("adding issues.%s for migration 0053: %w", col.name, err)
		}
	}
	return nil
}
