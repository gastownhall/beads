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
	auxiliaryDDL := []string{
		schema.WispLabelsSchema,
		schema.WispDependenciesSchema,
		schema.WispEventsSchema,
		schema.WispCommentsSchema,
	}

	for _, ddl := range auxiliaryDDL {
		if _, err := db.Exec(ddl); err != nil {
			return fmt.Errorf("failed to create wisp auxiliary table: %w", err)
		}
	}

	return nil
}
