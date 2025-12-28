package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateMessagingFields adds messaging and graph link support columns to the issues table.
// These columns support inter-agent communication:
// - sender: who sent this message
// - ephemeral: can be bulk-deleted when closed
// - replies_to: issue ID for conversation threading
// - relates_to: JSON array of issue IDs for knowledge graph edges
// - duplicate_of: canonical issue ID (this is a duplicate)
// - superseded_by: replacement issue ID (this is obsolete)
func MigrateMessagingFields(db *sql.DB) error {
	columns := []struct {
		name       string
		definition string
	}{
		{"sender", "TEXT DEFAULT ''"},
		{"ephemeral", "INTEGER DEFAULT 0"},
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

	// Add index for ephemeral issues (for efficient cleanup queries)
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_ephemeral ON issues(ephemeral) WHERE ephemeral = 1`)
	if err != nil {
		return fmt.Errorf("failed to create ephemeral index: %w", err)
	}

	// Add index for sender (for efficient inbox queries)
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_sender ON issues(sender) WHERE sender != ''`)
	if err != nil {
		return fmt.Errorf("failed to create sender index: %w", err)
	}

	return nil
}
