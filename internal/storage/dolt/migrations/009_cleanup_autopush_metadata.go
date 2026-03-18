package migrations

import (
	"context"
	"fmt"
)

// MigrateCleanupAutopushMetadata removes stale dolt_auto_push_* rows from the
// metadata table. These machine-local values were previously stored in Dolt
// history, causing recurring merge conflicts on multi-machine setups (GH#2466).
// The auto-push state is now tracked in a local file (.beads/push-state.json).
func MigrateCleanupAutopushMetadata(ctx context.Context, db Runner) error {
	exists, err := tableExists(ctx, db, "metadata")
	if err != nil {
		return fmt.Errorf("failed to check metadata table existence: %w", err)
	}
	if !exists {
		return nil
	}

	_, err = execContext(ctx, db, "DELETE FROM metadata WHERE `key` LIKE 'dolt_auto_push_%'")
	if err != nil {
		return fmt.Errorf("failed to delete dolt_auto_push rows from metadata: %w", err)
	}
	return nil
}
