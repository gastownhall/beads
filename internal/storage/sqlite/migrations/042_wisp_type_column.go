package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateWispTypeColumn adds the wisp_type column to the issues table.
// This classifies ephemeral wisps for TTL-based compaction (gt-9br).
// Supported types: heartbeat, ping, patrol, gc_report, recovery, error, escalation.
// See WISP-COMPACTION-POLICY.md for TTL assignments.
func MigrateWispTypeColumn(db *sql.DB) error {
	// Check if column already exists
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'wisp_type'
	`).Scan(&columnExists)
	if err != nil {
		return fmt.Errorf("failed to check wisp_type column: %w", err)
	}

	if columnExists {
		return nil
	}

	// Add the wisp_type column (TEXT, default empty string for unclassified wisps)
	_, err = db.Exec(`ALTER TABLE issues ADD COLUMN wisp_type TEXT DEFAULT ''`)
	if err != nil {
		return fmt.Errorf("failed to add wisp_type column: %w", err)
	}

	return nil
}
