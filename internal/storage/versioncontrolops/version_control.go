package versioncontrolops

import (
	"context"
	"fmt"
	"regexp"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// validTablePattern matches valid SQL table names (letters, digits, underscores).
var validTablePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Status returns the current Dolt working set status (staged and unstaged changes).
func Status(ctx context.Context, db DBConn) (*storage.Status, error) {
	rows, err := db.QueryContext(ctx, "SELECT table_name, staged, status FROM dolt_status")
	if err != nil {
		return nil, fmt.Errorf("get status: %w", err)
	}
	defer rows.Close()

	status := &storage.Status{
		Staged:   make([]storage.StatusEntry, 0),
		Unstaged: make([]storage.StatusEntry, 0),
	}

	for rows.Next() {
		var tableName string
		var staged bool
		var statusStr string
		if err := rows.Scan(&tableName, &staged, &statusStr); err != nil {
			return nil, fmt.Errorf("scan status: %w", err)
		}
		entry := storage.StatusEntry{Table: tableName, Status: statusStr}
		if staged {
			status.Staged = append(status.Staged, entry)
		} else {
			status.Unstaged = append(status.Unstaged, entry)
		}
	}
	return status, rows.Err()
}

// Log returns recent commit history up to limit entries.
// If limit is 0 or negative, all entries are returned.
func Log(ctx context.Context, db DBConn, limit int) ([]storage.CommitInfo, error) {
	var query string
	var args []interface{}
	if limit > 0 {
		query = "SELECT commit_hash, committer, email, date, message FROM dolt_log ORDER BY date DESC LIMIT ?"
		args = []interface{}{limit}
	} else {
		query = "SELECT commit_hash, committer, email, date, message FROM dolt_log ORDER BY date DESC"
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get log: %w", err)
	}
	defer rows.Close()

	var commits []storage.CommitInfo
	for rows.Next() {
		var c storage.CommitInfo
		if err := rows.Scan(&c.Hash, &c.Author, &c.Email, &c.Date, &c.Message); err != nil {
			return nil, fmt.Errorf("scan commit: %w", err)
		}
		commits = append(commits, c)
	}
	return commits, rows.Err()
}

// CommitExists checks whether a commit hash (or prefix) exists in dolt_log.
// Returns false for empty strings or malformed input.
func CommitExists(ctx context.Context, db DBConn, commitHash string) (bool, error) {
	if commitHash == "" {
		return false, nil
	}
	if err := issueops.ValidateRef(commitHash); err != nil {
		return false, nil
	}

	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_log
		WHERE commit_hash = ? OR commit_hash LIKE ?
	`, commitHash, commitHash+"%").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check commit existence: %w", err)
	}
	return count > 0, nil
}

// Merge merges the named branch into the current branch. The author string
// should be formatted as "Name <email>". Returns any merge conflicts.
func Merge(ctx context.Context, db DBConn, branch, author string) ([]storage.Conflict, error) {
	_, err := db.ExecContext(ctx, "CALL DOLT_MERGE('--author', ?, ?)", author, branch)
	if err != nil {
		// Check if the error is due to conflicts.
		conflicts, conflictErr := GetConflicts(ctx, db)
		if conflictErr == nil && len(conflicts) > 0 {
			return conflicts, nil
		}
		return nil, fmt.Errorf("merge branch %s: %w", branch, err)
	}
	return nil, nil
}

// GetConflicts returns any merge conflicts in the current Dolt state.
func GetConflicts(ctx context.Context, db DBConn) ([]storage.Conflict, error) {
	rows, err := db.QueryContext(ctx, "SELECT `table`, num_conflicts FROM dolt_conflicts")
	if err != nil {
		return nil, fmt.Errorf("get conflicts: %w", err)
	}
	defer rows.Close()

	var conflicts []storage.Conflict
	for rows.Next() {
		var tableName string
		var numConflicts int
		if err := rows.Scan(&tableName, &numConflicts); err != nil {
			return nil, fmt.Errorf("scan conflict: %w", err)
		}
		conflicts = append(conflicts, storage.Conflict{
			Field: tableName,
		})
		_ = numConflicts // available if needed in the future
	}
	return conflicts, rows.Err()
}

// ResolveConflicts resolves conflicts for a table using the given strategy
// ("ours" or "theirs").
func ResolveConflicts(ctx context.Context, db DBConn, table, strategy string) error {
	if err := validateTableName(table); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}

	var query string
	switch strategy {
	case "ours":
		query = fmt.Sprintf("CALL DOLT_CONFLICTS_RESOLVE('--ours', '%s')", table)
	case "theirs":
		query = fmt.Sprintf("CALL DOLT_CONFLICTS_RESOLVE('--theirs', '%s')", table)
	default:
		return fmt.Errorf("unknown conflict resolution strategy: %s", strategy)
	}

	if _, err := db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("resolve conflicts: %w", err)
	}
	return nil
}

func validateTableName(table string) error {
	if table == "" {
		return fmt.Errorf("table name cannot be empty")
	}
	if len(table) > 64 {
		return fmt.Errorf("table name too long")
	}
	if !validTablePattern.MatchString(table) {
		return fmt.Errorf("invalid table name: %s", table)
	}
	return nil
}
