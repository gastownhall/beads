package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateWispDepTypeIndex adds indices on the type column of wisp_dependencies
// to speed up blocker computation queries that filter by type.
// Without this index, queries like SELECT ... WHERE type IN ('blocks', 'waits-for', ...)
// perform a full table scan of potentially 30K+ rows.
//
// Idempotent: checks for existing indices before creating.
func MigrateWispDepTypeIndex(db *sql.DB) error {
	exists, err := TableExists(db, "wisp_dependencies")
	if err != nil {
		return fmt.Errorf("checking wisp_dependencies table: %w", err)
	}
	if !exists {
		return nil
	}

	indices := []struct {
		name string
		ddl  string
	}{
		{
			name: "idx_wisp_dep_type",
			ddl:  "CREATE INDEX idx_wisp_dep_type ON wisp_dependencies (type)",
		},
		{
			name: "idx_wisp_dep_type_depends",
			ddl:  "CREATE INDEX idx_wisp_dep_type_depends ON wisp_dependencies (type, depends_on_id)",
		},
	}

	for _, idx := range indices {
		if indexExists(db, "wisp_dependencies", idx.name) {
			continue
		}
		if _, err := db.Exec(idx.ddl); err != nil {
			return fmt.Errorf("creating index %s: %w", idx.name, err)
		}
	}
	return nil
}
