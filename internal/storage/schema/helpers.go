package schema

import (
	"context"
	"fmt"
)

// EnsureIgnoredTables checks whether the dolt_ignore'd wisp tables exist in
// the current working set and creates them if missing. This is the fast path
// called after branch creation, checkout, and on session init — it executes a
// single SHOW TABLES query and returns immediately when the tables are present.
//
// dolt_ignore entries are committed and persist across branches; only the
// tables themselves (which live in the working set) need recreation.
func EnsureIgnoredTables(ctx context.Context, db DBConn) error {
	wispsOK, err := TableExists(ctx, db, "wisps")
	if err != nil {
		return fmt.Errorf("check wisps table: %w", err)
	}
	localOK, err := TableExists(ctx, db, "local_metadata")
	if err != nil {
		return fmt.Errorf("check local_metadata table: %w", err)
	}
	if wispsOK && localOK {
		return nil
	}
	return CreateIgnoredTables(ctx, db)
}

// CreateIgnoredTables unconditionally creates all dolt_ignore'd tables
// (wisps, wisp_labels, wisp_dependencies, wisp_events, wisp_comments).
// All statements use CREATE TABLE IF NOT EXISTS, so this is idempotent.
//
// This does NOT set up dolt_ignore entries or commit — those are migration
// concerns handled separately during bd init.
func CreateIgnoredTables(ctx context.Context, db DBConn) error {
	for _, ddl := range IgnoredTableDDL() {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			// Tolerate "already exists" / "duplicate column" errors: when
			// wisps exists but local_metadata doesn't (or vice versa), the
			// ALTER TABLE statements for existing tables may hit columns
			// that are already present.
			if !isConcurrentInitError(err) {
				return fmt.Errorf("create ignored table: %w", err)
			}
		}
	}
	return nil
}

// TableExists checks if a table exists using SHOW TABLES LIKE.
// Uses SHOW TABLES rather than information_schema to avoid crashes when the
// Dolt server catalog contains stale database entries from cleaned-up
// worktrees (GH#2051). SHOW TABLES is inherently scoped to the current
// database.
func TableExists(ctx context.Context, db DBConn, table string) (bool, error) {
	// Use string interpolation because Dolt doesn't support prepared-statement
	// parameters for SHOW commands. Table names come from internal constants.
	// #nosec G202 -- table names come from internal constants, not user input.
	rows, err := db.QueryContext(ctx, "SHOW TABLES LIKE '"+table+"'") //nolint:gosec // G202: table name is an internal constant
	if err != nil {
		return false, fmt.Errorf("check table %s: %w", table, err)
	}
	defer rows.Close()
	return rows.Next(), nil
}
