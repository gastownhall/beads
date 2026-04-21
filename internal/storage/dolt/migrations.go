package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// CreateIgnoredTables re-creates dolt_ignore'd tables (wisps, wisp_*)
// on the current branch. These tables only exist in the working set and
// are not inherited when branching. Safe to call repeatedly (idempotent).
// Exported for use by test helpers in other packages.
func CreateIgnoredTables(db *sql.DB) error {
	return schema.CreateIgnoredTables(context.Background(), db)
}
