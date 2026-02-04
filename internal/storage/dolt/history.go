//go:build cgo && !windows
package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// validRefPattern matches valid Dolt commit hashes (32 hex chars) or branch names
var validRefPattern = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

// validTablePattern matches valid table names
var validTablePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

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

// IssueHistory represents an issue at a specific point in history
type IssueHistory struct {
	Issue      *types.Issue
	CommitHash string
	Committer  string
	CommitDate time.Time
}

// GetIssueHistory returns the complete history of an issue
func (s *DoltStore) GetIssueHistory(ctx context.Context, issueID string) ([]*IssueHistory, error) {
	rows, err := s.db.QueryContext(ctx, `
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

	var history []*IssueHistory
	for rows.Next() {
		var h IssueHistory
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

// GetIssueAsOf returns an issue as it existed at a specific commit or time
func (s *DoltStore) GetIssueAsOf(ctx context.Context, issueID string, ref string) (*types.Issue, error) {
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
		return nil, nil
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

// DiffEntry represents a change between two commits
type DiffEntry struct {
	TableName  string
	DiffType   string // "added", "modified", "removed"
	FromCommit string
	ToCommit   string
	RowID      string
}

// GetDiff returns changes between two commits
func (s *DoltStore) GetDiff(ctx context.Context, fromRef, toRef string) ([]*DiffEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT table_name, diff_type, from_commit, to_commit
		FROM dolt_diff(?, ?)
	`, fromRef, toRef)
	if err != nil {
		return nil, fmt.Errorf("failed to get diff: %w", err)
	}
	defer rows.Close()

	var entries []*DiffEntry
	for rows.Next() {
		var e DiffEntry
		if err := rows.Scan(&e.TableName, &e.DiffType, &e.FromCommit, &e.ToCommit); err != nil {
			return nil, fmt.Errorf("failed to scan diff entry: %w", err)
		}
		entries = append(entries, &e)
	}

	return entries, rows.Err()
}

// GetIssueDiff returns detailed changes to a specific issue between commits
func (s *DoltStore) GetIssueDiff(ctx context.Context, issueID, fromRef, toRef string) (*IssueDiff, error) {
	// Validate refs to prevent SQL injection
	if err := validateRef(fromRef); err != nil {
		return nil, fmt.Errorf("invalid fromRef: %w", err)
	}
	if err := validateRef(toRef); err != nil {
		return nil, fmt.Errorf("invalid toRef: %w", err)
	}

	// nolint:gosec // G201: refs are validated by validateRef() above
	// Syntax: dolt_diff(from_ref, to_ref, 'table_name')
	query := fmt.Sprintf(`
		SELECT
			from_id, to_id,
			from_title, to_title,
			from_status, to_status,
			from_description, to_description,
			diff_type
		FROM dolt_diff('%s', '%s', 'issues')
		WHERE from_id = ? OR to_id = ?
	`, fromRef, toRef)

	var diff IssueDiff
	var fromID, toID, fromTitle, toTitle, fromStatus, toStatus sql.NullString
	var fromDesc, toDesc sql.NullString

	err := s.db.QueryRowContext(ctx, query, issueID, issueID).Scan(
		&fromID, &toID,
		&fromTitle, &toTitle,
		&fromStatus, &toStatus,
		&fromDesc, &toDesc,
		&diff.DiffType,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue diff: %w", err)
	}

	if fromID.Valid {
		diff.FromID = fromID.String
	}
	if toID.Valid {
		diff.ToID = toID.String
	}
	if fromTitle.Valid {
		diff.FromTitle = fromTitle.String
	}
	if toTitle.Valid {
		diff.ToTitle = toTitle.String
	}
	if fromStatus.Valid {
		diff.FromStatus = fromStatus.String
	}
	if toStatus.Valid {
		diff.ToStatus = toStatus.String
	}
	if fromDesc.Valid {
		diff.FromDescription = fromDesc.String
	}
	if toDesc.Valid {
		diff.ToDescription = toDesc.String
	}

	return &diff, nil
}

// IssueDiff represents changes to an issue between two commits
type IssueDiff struct {
	DiffType        string // "added", "modified", "removed"
	FromID          string
	ToID            string
	FromTitle       string
	ToTitle         string
	FromStatus      string
	ToStatus        string
	FromDescription string
	ToDescription   string
}

// GetInternalConflicts returns any merge conflicts in the current state (internal format).
// For the public interface, use GetConflicts which returns storage.Conflict.
func (s *DoltStore) GetInternalConflicts(ctx context.Context) ([]*TableConflict, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT table_name, num_conflicts FROM dolt_conflicts
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get conflicts: %w", err)
	}
	defer rows.Close()

	var conflicts []*TableConflict
	for rows.Next() {
		var c TableConflict
		if err := rows.Scan(&c.TableName, &c.NumConflicts); err != nil {
			return nil, fmt.Errorf("failed to scan conflict: %w", err)
		}
		conflicts = append(conflicts, &c)
	}

	return conflicts, rows.Err()
}

// TableConflict represents a Dolt table-level merge conflict (internal representation).
type TableConflict struct {
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

	_, err := s.db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to resolve conflicts: %w", err)
	}
	return nil
}

