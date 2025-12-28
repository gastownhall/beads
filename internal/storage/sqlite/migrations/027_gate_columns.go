package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateGateColumns adds gate-related columns to the issues table for async coordination.
// Gate fields enable agents to wait on external conditions (CI completion, human approval, etc.)
func MigrateGateColumns(db *sql.DB) error {
	columns := []struct {
		name    string
		sqlType string
	}{
		{"await_type", "TEXT"},          // Condition type: gh:run, gh:pr, timer, human, mail
		{"await_id", "TEXT"},            // Condition identifier (e.g., run ID, PR number)
		{"timeout_ns", "INTEGER"},       // Timeout in nanoseconds (Go's time.Duration)
		{"waiters", "TEXT"},             // JSON array of mail addresses to notify
	}

	for _, col := range columns {
		// Check if column already exists
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

		// Add the column
		_, err = db.Exec(fmt.Sprintf(`ALTER TABLE issues ADD COLUMN %s %s`, col.name, col.sqlType))
		if err != nil {
			return fmt.Errorf("failed to add %s column: %w", col.name, err)
		}
	}

	// Add index for gate type issues (for efficient gate queries)
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_issues_gate ON issues(issue_type) WHERE issue_type = 'gate'`)
	if err != nil {
		return fmt.Errorf("failed to create gate index: %w", err)
	}

	return nil
}
