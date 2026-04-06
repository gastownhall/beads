package migrations

import (
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// MigrateWispAuxiliaryTables creates auxiliary tables for wisps: labels,
// dependencies, events, and comments. These mirror the corresponding main
// tables but reference the wisps table instead of issues. They are covered
// by the dolt_ignore pattern "wisp_%" added in migration 004.
func MigrateWispAuxiliaryTables(db *sql.DB) error {
	auxiliaryDDL := []string{
		versioncontrolops.WispLabelsSchema,
		versioncontrolops.WispDependenciesSchema,
		versioncontrolops.WispEventsSchema,
		versioncontrolops.WispCommentsSchema,
	}

	for _, ddl := range auxiliaryDDL {
		if _, err := db.Exec(ddl); err != nil {
			return fmt.Errorf("failed to create wisp auxiliary table: %w", err)
		}
	}

	return nil
}
