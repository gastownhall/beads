package migrations

import (
	"database/sql"
	"fmt"
)

// MigrateIssuesTypeStatusIndex adds a composite index on (issue_type, status)
// to the issues table. This is a covering index for CountByStatus queries
// (SELECT status, COUNT(*) FROM issues WHERE issue_type IN (...) GROUP BY status)
// which previously caused full-table scans. Under multi-agent load, 15+
// concurrent CountByStatus queries would pile up and block other Dolt clients.
//
// Idempotent: checks for existing index before creating.
func MigrateIssuesTypeStatusIndex(db *sql.DB) error {
	exists, err := tableExists(db, "issues")
	if err != nil {
		return fmt.Errorf("checking issues table: %w", err)
	}
	if !exists {
		return nil
	}

	if indexExists(db, "issues", "idx_issues_type_status") {
		return nil
	}
	if _, err := db.Exec("CREATE INDEX idx_issues_type_status ON issues (issue_type, status)"); err != nil {
		return fmt.Errorf("creating index idx_issues_type_status: %w", err)
	}
	return nil
}
