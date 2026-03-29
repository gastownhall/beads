package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateWispEventsCreatedAtIndex adds an index on wisp_events.created_at to
// match the idx_events_created_at index on the events table.
//
// Without this index, GetAllEventsSince's UNION ALL query performs a full table
// scan on wisp_events for the WHERE created_at > ? predicate. The events table
// already has this index (added in the original schema), but wisp_events was
// created in migration 005 without it.
//
// This also prevents the Dolt +Inf cast error described in GH#2760: when the
// optimizer lacks an index on the comparison column, it may choose a code path
// that implicitly casts CHAR(36) UUID values to float64, causing overflow on
// UUIDs that resemble scientific notation (e.g. 001e914b... → 1e914 → +Inf).
func MigrateWispEventsCreatedAtIndex(db *sql.DB) error {
	exists, err := tableExists(db, "wisp_events")
	if err != nil {
		return fmt.Errorf("checking wisp_events: %w", err)
	}
	if !exists {
		return nil
	}

	if indexExists(db, "wisp_events", "idx_wisp_events_created_at") {
		return nil
	}

	_, err = db.Exec("CREATE INDEX idx_wisp_events_created_at ON wisp_events (created_at)")
	if err != nil {
		return fmt.Errorf("creating idx_wisp_events_created_at: %w", err)
	}

	return nil
}
