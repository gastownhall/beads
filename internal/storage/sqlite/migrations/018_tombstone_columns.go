package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateTombstoneColumns is a legacy migration that adds soft-delete columns.
// These columns are no longer used (Dolt handles delete propagation natively)
// but must remain for existing databases that already have this migration recorded.
func MigrateTombstoneColumns(db *sql.DB) error {
	columns := []struct {
		name       string
		definition string
	}{
		{"deleted_at", "DATETIME"},
		{"deleted_by", "TEXT DEFAULT ''"},
		{"delete_reason", "TEXT DEFAULT ''"},
		{"original_type", "TEXT DEFAULT ''"},
	}

	for _, col := range columns {
		var columnExists bool
		err := db.QueryRow(`
			SELECT COUNT(*) > 0
			FROM pragma_table_info('issues')
			WHERE name = ?
		`, col.name).Scan(&columnExists)
		if err != nil {
			return fmt.Errorf("failed to check %s column: %w", col.name, err)
		}

		if columnExists {
			continue
		}

		_, err = db.Exec(fmt.Sprintf(`ALTER TABLE issues ADD COLUMN %s %s`, col.name, col.definition))
		if err != nil {
			return fmt.Errorf("failed to add %s column: %w", col.name, err)
		}
	}

	// Add partial index on deleted_at for efficient TTL queries
	// Only indexes non-NULL values (legacy, no longer used)
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_deleted_at ON issues(deleted_at) WHERE deleted_at IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("failed to create deleted_at index: %w", err)
	}

	return nil
}
