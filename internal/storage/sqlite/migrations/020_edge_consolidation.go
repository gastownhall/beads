package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateEdgeConsolidation adds metadata and thread_id columns to the dependencies table.
// This is Phase 1 of the Edge Schema Consolidation (Decision 004):
// - metadata: JSON blob for type-specific edge data (similarity scores, reasons, etc.)
// - thread_id: For efficient conversation threading queries
//
// These columns enable:
// - Edge metadata without schema changes (extensibility)
// - O(1) thread queries for Reddit-style conversations
// - HOP knowledge graph foundation
func MigrateEdgeConsolidation(db *sql.DB) error {
	columns := []struct {
		name       string
		definition string
	}{
		{"metadata", "TEXT DEFAULT '{}'"},
		{"thread_id", "TEXT DEFAULT ''"},
	}

	for _, col := range columns {
		var columnExists bool
		err := db.QueryRow(`
			SELECT COUNT(*) > 0
			FROM pragma_table_info('dependencies')
			WHERE name = ?
		`, col.name).Scan(&columnExists)
		if err != nil {
			return fmt.Errorf("failed to check %s column: %w", col.name, err)
		}

		if columnExists {
			continue
		}

		_, err = db.Exec(fmt.Sprintf(`ALTER TABLE dependencies ADD COLUMN %s %s`, col.name, col.definition))
		if err != nil {
			return fmt.Errorf("failed to add %s column: %w", col.name, err)
		}
	}

	// Add index for thread queries - only index non-empty thread_ids
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_dependencies_thread ON dependencies(thread_id) WHERE thread_id != ''`)
	if err != nil {
		return fmt.Errorf("failed to create thread_id index: %w", err)
	}

	// Add composite index for finding all edges in a thread by type
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_dependencies_thread_type ON dependencies(thread_id, type) WHERE thread_id != ''`)
	if err != nil {
		return fmt.Errorf("failed to create thread_type index: %w", err)
	}

	return nil
}
