package migrations

import (
	"context"
	"fmt"
)

// MigrateWispTypeColumn adds the wisp_type column to the issues table.
// This column classifies wisps for TTL-based compaction (gt-9br).
// New databases already have this column from the schema definition;
// this migration handles databases created before it was added.
func MigrateWispTypeColumn(ctx context.Context, db Runner) error {
	exists, err := columnExists(ctx, db, "issues", "wisp_type")
	if err != nil {
		return fmt.Errorf("failed to check wisp_type column: %w", err)
	}
	if exists {
		return nil
	}

	_, err = execContext(ctx, db, `ALTER TABLE issues ADD COLUMN wisp_type VARCHAR(32) DEFAULT ''`)
	if err != nil {
		return fmt.Errorf("failed to add wisp_type column: %w", err)
	}

	return nil
}
