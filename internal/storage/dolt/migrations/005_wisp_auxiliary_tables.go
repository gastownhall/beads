package migrations

import (
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// MigrateWispAuxiliaryTables creates auxiliary tables for wisps: labels,
// dependencies, events, and comments. These mirror the corresponding main
// tables but reference the wisps table instead of issues. They are covered
// by the dolt_ignore pattern "wisp_%" added in migration 004.
func MigrateWispAuxiliaryTables(db *sql.DB) error {
	// Migration 0021 is a multi-statement file that creates all four
	// auxiliary tables. The Dolt/MySQL driver has multiStatements=true.
	if _, err := db.Exec(schema.ReadMigrationSQL(21)); err != nil {
		return fmt.Errorf("failed to create wisp auxiliary tables: %w", err)
	}
	return nil
}
