package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// validRefPattern matches valid Dolt commit hashes (32 hex chars) or branch names
var validRefPattern = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

// validTablePattern matches valid table names
var validTablePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// validDatabasePattern matches valid MySQL database names (alphanumeric, underscore, hyphen)
var validDatabasePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_\-]*$`)

// validateRef checks if a ref is safe to use in queries
func validateRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("ref cannot be empty")
	}
	if len(ref) > 128 {
		return fmt.Errorf("ref too long")
	}
	if !validRefPattern.MatchString(ref) {
		return fmt.Errorf("invalid ref format: %s", ref)
	}
	return nil
}

// validateDatabaseName checks if a database name is safe to use in queries.
// Prevents SQL injection via backtick escaping in CREATE DATABASE statements.
func validateDatabaseName(name string) error {
	if name == "" {
		return fmt.Errorf("database name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("database name too long")
	}
	if !validDatabasePattern.MatchString(name) {
		return fmt.Errorf("invalid database name: %s", name)
	}
	return nil
}

// validateTableName checks if a table name is safe to use in queries
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

// issueHistory represents an issue at a specific point in history
type issueHistory struct {
	Issue      *types.Issue
	CommitHash string
	Committer  string
	CommitDate time.Time
}

// getIssueHistory returns the complete history of an issue
func (s *DoltStore) getIssueHistory(ctx context.Context, issueID string) ([]*issueHistory, error) {
	rows, err := s.queryContext(ctx, `
		SELECT
			id, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, owner, created_by,
			estimated_minutes, created_at, updated_at, closed_at, close_reason,
			pinned, mol_type,
			commit_hash, committer, commit_date
		FROM dolt_history_issues
		WHERE id = ?
		ORDER BY commit_date DESC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue history: %w", err)
	}
	defer rows.Close()

	var history []*issueHistory
	for rows.Next() {
		var h issueHistory
		var issue types.Issue
		var createdAtStr, updatedAtStr sql.NullString // TEXT columns - must parse manually
		var closedAt sql.NullTime
		var assignee, owner, createdBy, closeReason, molType sql.NullString
		var estimatedMinutes sql.NullInt64
		var pinned sql.NullInt64

		if err := rows.Scan(
			&issue.ID, &issue.Title, &issue.Description, &issue.Design, &issue.AcceptanceCriteria, &issue.Notes,
			&issue.Status, &issue.Priority, &issue.IssueType, &assignee, &owner, &createdBy,
			&estimatedMinutes, &createdAtStr, &updatedAtStr, &closedAt, &closeReason,
			&pinned, &molType,
			&h.CommitHash, &h.Committer, &h.CommitDate,
		); err != nil {
			return nil, fmt.Errorf("failed to scan history: %w", err)
		}

		// Parse timestamp strings (TEXT columns require manual parsing)
		if createdAtStr.Valid {
			issue.CreatedAt = parseTimeString(createdAtStr.String)
		}
		if updatedAtStr.Valid {
			issue.UpdatedAt = parseTimeString(updatedAtStr.String)
		}

		if closedAt.Valid {
			issue.ClosedAt = &closedAt.Time
		}
		if assignee.Valid {
			issue.Assignee = assignee.String
		}
		if owner.Valid {
			issue.Owner = owner.String
		}
		if createdBy.Valid {
			issue.CreatedBy = createdBy.String
		}
		if estimatedMinutes.Valid {
			mins := int(estimatedMinutes.Int64)
			issue.EstimatedMinutes = &mins
		}
		if closeReason.Valid {
			issue.CloseReason = closeReason.String
		}
		if pinned.Valid && pinned.Int64 != 0 {
			issue.Pinned = true
		}
		if molType.Valid {
			issue.MolType = types.MolType(molType.String)
		}

		h.Issue = &issue
		history = append(history, &h)
	}

	return history, rows.Err()
}

// getIssueAsOf returns an issue as it existed at a specific commit or time
func (s *DoltStore) getIssueAsOf(ctx context.Context, issueID string, ref string) (*types.Issue, error) {
	// Validate ref to prevent SQL injection
	if err := validateRef(ref); err != nil {
		return nil, fmt.Errorf("invalid ref: %w", err)
	}

	var issue types.Issue
	var createdAtStr, updatedAtStr sql.NullString // TEXT columns - must parse manually
	var closedAt sql.NullTime
	var assignee, owner, contentHash sql.NullString
	var estimatedMinutes sql.NullInt64

	// nolint:gosec // G201: ref is validated by validateRef() above - AS OF requires literal
	query := fmt.Sprintf(`
		SELECT id, content_hash, title, description, status, priority, issue_type, assignee, estimated_minutes,
		       created_at, created_by, owner, updated_at, closed_at
		FROM issues AS OF '%s'
		WHERE id = ?
	`, ref)

	err := s.db.QueryRowContext(ctx, query, issueID).Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Status, &issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&createdAtStr, &issue.CreatedBy, &owner, &updatedAtStr, &closedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: issue %s as of %s", storage.ErrNotFound, issueID, ref)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue as of %s: %w", ref, err)
	}

	// Parse timestamp strings (TEXT columns require manual parsing)
	if createdAtStr.Valid {
		issue.CreatedAt = parseTimeString(createdAtStr.String)
	}
	if updatedAtStr.Valid {
		issue.UpdatedAt = parseTimeString(updatedAtStr.String)
	}

	if contentHash.Valid {
		issue.ContentHash = contentHash.String
	}
	if closedAt.Valid {
		issue.ClosedAt = &closedAt.Time
	}
	if assignee.Valid {
		issue.Assignee = assignee.String
	}
	if owner.Valid {
		issue.Owner = owner.String
	}
	if estimatedMinutes.Valid {
		mins := int(estimatedMinutes.Int64)
		issue.EstimatedMinutes = &mins
	}

	return &issue, nil
}

// getInternalConflicts returns any merge conflicts in the current state (internal format).
// For the public interface, use GetConflicts which returns storage.Conflict.
func (s *DoltStore) getInternalConflicts(ctx context.Context) ([]*tableConflict, error) {
	rows, err := s.queryContext(ctx,
		"SELECT `table`, num_conflicts FROM dolt_conflicts")
	if err != nil {
		return nil, fmt.Errorf("failed to get conflicts: %w", err)
	}
	defer rows.Close()

	var conflicts []*tableConflict
	for rows.Next() {
		var c tableConflict
		if err := rows.Scan(&c.TableName, &c.NumConflicts); err != nil {
			return nil, fmt.Errorf("failed to scan conflict: %w", err)
		}
		conflicts = append(conflicts, &c)
	}

	return conflicts, rows.Err()
}

// tableConflict represents a Dolt table-level merge conflict (internal representation).
type tableConflict struct {
	TableName    string
	NumConflicts int
}

// ResolveConflicts resolves conflicts using the specified strategy
func (s *DoltStore) ResolveConflicts(ctx context.Context, table string, strategy string) error {
	// Validate table name to prevent SQL injection
	if err := validateTableName(table); err != nil {
		return fmt.Errorf("invalid table name: %w", err)
	}

	var query string
	switch strategy {
	case "ours":
		// Note: DOLT_CONFLICTS_RESOLVE requires literal value, but we've validated table is safe
		query = fmt.Sprintf("CALL DOLT_CONFLICTS_RESOLVE('--ours', '%s')", table)
	case "theirs":
		query = fmt.Sprintf("CALL DOLT_CONFLICTS_RESOLVE('--theirs', '%s')", table)
	default:
		return fmt.Errorf("unknown conflict resolution strategy: %s", strategy)
	}

	_, err := s.execContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to resolve conflicts: %w", err)
	}
	return nil
}
