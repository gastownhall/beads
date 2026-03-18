package migrations

import (
	"context"
	"fmt"
)

// MigrateIssueCounterTable creates the issue_counter table used for
// sequential issue ID generation when issue_id_mode=counter is configured.
// The table stores one row per prefix, tracking the last assigned integer.
func MigrateIssueCounterTable(ctx context.Context, db Runner) error {
	exists, err := tableExists(ctx, db, "issue_counter")
	if err != nil {
		return fmt.Errorf("failed to check issue_counter existence: %w", err)
	}
	if exists {
		return nil
	}

	_, err = execContext(ctx, db, `CREATE TABLE issue_counter (
    prefix VARCHAR(255) PRIMARY KEY,
    last_id INT NOT NULL DEFAULT 0
)`)
	if err != nil {
		return fmt.Errorf("failed to create issue_counter table: %w", err)
	}

	return nil
}
