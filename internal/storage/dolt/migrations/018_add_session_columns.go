package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateAddSessionColumns ensures the created_by_session and claimed_by_session
// columns exist on the issues and wisps tables. These columns mirror the
// per-event session attribution pattern established by closed_by_session:
// each lifecycle event (create / claim / close) has its own dedicated column
// naming the Claude Code session responsible, with the audit log remaining
// the source of truth for full multi-session history.
//
// This compat migration repairs databases that predate schema migration 0033.
// Safe to run unconditionally: each ALTER is gated by a SHOW COLUMNS check.
func MigrateAddSessionColumns(db *sql.DB) error {
	cols := []string{"created_by_session", "claimed_by_session"}
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
		for _, col := range cols {
			exists, err := columnExists(db, table, col)
			if err != nil {
				return fmt.Errorf("failed to check %s column on %s: %w", col, table, err)
			}
			if exists {
				continue
			}
			//nolint:gosec // G201: table and col are from hardcoded lists
			if _, err := db.Exec(fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN %s VARCHAR(255) DEFAULT ''", table, col)); err != nil {
				return fmt.Errorf("failed to add %s column to %s: %w", col, table, err)
			}
		}
	}
	return nil
}
