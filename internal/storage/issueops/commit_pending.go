package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// HasPendingChanges checks whether there are any committable changes in the
// Dolt working set, excluding tables matched by dolt_ignore.
func HasPendingChanges(ctx context.Context, db interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status s
		WHERE NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check status: %w", err)
	}
	return count > 0, nil
}

// BuildBatchCommitMessage generates a descriptive commit message summarizing
// what changed since the last commit by querying dolt_diff against HEAD.
// It reports issue-level create/update/delete counts and lists any other
// tables (labels, comments, events, etc.) that have uncommitted changes.
func BuildBatchCommitMessage(ctx context.Context, db interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, actor string) string {
	if actor == "" {
		actor = "bd"
	}
	// Count issue-level changes by diff type
	var added, modified, removed int
	rows, err := db.QueryContext(ctx, `
		SELECT diff_type, COUNT(*) as cnt
		FROM dolt_diff('HEAD', 'WORKING', 'issues')
		GROUP BY diff_type
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var diffType string
			var count int
			if scanErr := rows.Scan(&diffType, &count); scanErr == nil {
				switch diffType {
				case "added":
					added = count
				case "modified":
					modified = count
				case "removed":
					removed = count
				}
			}
		}
		_ = rows.Err() // Best effort
	}

	// Check which other tables have uncommitted changes beyond issues.
	var otherTables []string
	statusRows, statusErr := db.QueryContext(ctx, `
		SELECT table_name FROM dolt_status s
		WHERE table_name != 'issues'
		AND NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)`)
	if statusErr == nil {
		defer statusRows.Close()
		for statusRows.Next() {
			var table string
			if scanErr := statusRows.Scan(&table); scanErr == nil {
				otherTables = append(otherTables, table)
			}
		}
		_ = statusRows.Err() // Best effort
	}

	// Build descriptive message
	var parts []string
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d created", added))
	}
	if modified > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", modified))
	}
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", removed))
	}

	if len(parts) == 0 && len(otherTables) == 0 {
		return fmt.Sprintf("bd: batch commit by %s", actor)
	}

	msg := fmt.Sprintf("bd: batch commit by %s", actor)
	if len(parts) > 0 {
		msg += " — " + strings.Join(parts, ", ")
	}
	if len(otherTables) > 0 {
		msg += fmt.Sprintf(" (+ %s)", strings.Join(otherTables, ", "))
	}
	return msg
}

// IsNothingToCommitError returns true if the error indicates there was nothing
// to commit (Dolt may report this even when dolt_status showed changes).
func IsNothingToCommitError(err error) bool {
	if err == nil {
		return false
	}
	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "nothing to commit") || strings.Contains(errLower, "no changes")
}
