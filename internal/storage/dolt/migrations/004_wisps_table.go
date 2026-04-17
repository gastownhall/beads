package migrations

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// MigrateWispsTable adds dolt_ignore patterns for wisps tables and creates the
// wisps table. The dolt_ignore entry MUST be added before the table is created,
// because dolt_ignore only works on untracked tables. Once a table has been
// committed, the ignore pattern has no effect.
//
// The wisps table has the same schema as the issues table — it stores ephemeral
// "wisp" beads that should not be version-tracked in Dolt history.
func MigrateWispsTable(db *sql.DB) error {
	// Fast path: if wisps table already exists, nothing to do.
	// The dolt_ignore entries are committed and persist across sessions;
	// the table itself lives in the working set and just needs CREATE IF NOT EXISTS.
	exists, err := TableExists(db, "wisps")
	if err != nil {
		return fmt.Errorf("failed to check wisps table existence: %w", err)
	}
	if exists {
		return nil
	}

	// Step 1: Add dolt_ignore patterns BEFORE creating the table.
	// Use REPLACE to be idempotent (dolt_ignore has pattern as PK).
	for _, pattern := range []string{"wisps", "wisp_%"} {
		_, err := db.Exec("REPLACE INTO dolt_ignore VALUES (?, true)", pattern)
		if err != nil {
			return fmt.Errorf("failed to add %q to dolt_ignore: %w", pattern, err)
		}
	}

	// Explicitly stage dolt_ignore and commit so the ignore is active before table creation.
	_, err = db.Exec("CALL DOLT_ADD('dolt_ignore')")
	if err != nil {
		return fmt.Errorf("failed to stage dolt_ignore: %w", err)
	}
	_, err = db.Exec("CALL DOLT_COMMIT('-m', 'chore: add wisps patterns to dolt_ignore')")
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
		return fmt.Errorf("failed to commit dolt_ignore changes: %w", err)
	}

	// Step 2: Create wisps table using the embedded migration file.
	_, err = db.Exec(schema.ReadMigrationSQL(20))
	if err != nil {
		return fmt.Errorf("failed to create wisps table: %w", err)
	}

	return nil
}
