package ephemeral

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// CreateIssue inserts a new issue into the ephemeral store.
func (s *Store) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	now := time.Now().UTC()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = now
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = now
	}
	if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
		closedAt := now
		issue.ClosedAt = &closedAt
	}

	// Ensure ephemeral flag is set
	issue.Ephemeral = true

	return s.insertIssue(ctx, s.db, issue, actor)
}

// CreateIssues inserts multiple issues into the ephemeral store.
func (s *Store) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, issue := range issues {
		now := time.Now().UTC()
		if issue.CreatedAt.IsZero() {
			issue.CreatedAt = now
		}
		if issue.UpdatedAt.IsZero() {
			issue.UpdatedAt = now
		}
		issue.Ephemeral = true

		if err := s.insertIssue(ctx, tx, issue, actor); err != nil {
			return err
		}
	}

	return tx.Commit()
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) insertIssue(ctx context.Context, db execer, issue *types.Issue, actor string) error {
	if issue.CreatedBy == "" {
		issue.CreatedBy = actor
	}

	metadataStr := "{}"
	if len(issue.Metadata) > 0 {
		metadataStr = string(issue.Metadata)
	}

	waitersStr := ""
	if len(issue.Waiters) > 0 {
		waitersStr = strings.Join(issue.Waiters, ",")
	}

	var closedAtStr *string
	if issue.ClosedAt != nil {
		s := formatTime(*issue.ClosedAt)
		closedAtStr = &s
	}

	var compactedAtStr *string
	if issue.CompactedAt != nil {
		s := formatTime(*issue.CompactedAt)
		compactedAtStr = &s
	}

	var dueAtStr, deferUntilStr, lastActivityStr *string
	if issue.DueAt != nil {
		s := formatTime(*issue.DueAt)
		dueAtStr = &s
	}
	if issue.DeferUntil != nil {
		s := formatTime(*issue.DeferUntil)
		deferUntilStr = &s
	}
	if issue.LastActivity != nil {
		s := formatTime(*issue.LastActivity)
		lastActivityStr = &s
	}

	var extRef *string
	if issue.ExternalRef != nil {
		extRef = issue.ExternalRef
	}

	ephemeralInt := 0
	if issue.Ephemeral {
		ephemeralInt = 1
	}
	pinnedInt := 0
	if issue.Pinned {
		pinnedInt = 1
	}
	isTemplateInt := 0
	if issue.IsTemplate {
		isTemplateInt = 1
	}
	crystallizesInt := 0
	if issue.Crystallizes {
		crystallizesInt = 1
	}

	var qualityScore *float64
	if issue.QualityScore != nil {
		qs := float64(*issue.QualityScore)
		qualityScore = &qs
	}

	_, err := db.ExecContext(ctx, `INSERT INTO issues (
		id, content_hash, title, description, design, acceptance_criteria, notes,
		status, priority, issue_type, assignee, estimated_minutes,
		created_at, created_by, owner, updated_at, closed_at, closed_by_session, external_ref, spec_id,
		compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
		sender, ephemeral, wisp_type, pinned, is_template, crystallizes,
		await_type, await_id, timeout_ns, waiters,
		hook_bead, role_bead, agent_state, last_activity, role_type, rig, mol_type,
		event_kind, actor, target, payload,
		due_at, defer_until,
		quality_score, work_type, source_system, metadata
	) VALUES (
		?, ?, ?, ?, ?, ?, ?,
		?, ?, ?, ?, ?,
		?, ?, ?, ?, ?, ?, ?, ?,
		?, ?, ?, ?, ?, ?,
		?, ?, ?, ?, ?, ?,
		?, ?, ?, ?,
		?, ?, ?, ?, ?, ?, ?,
		?, ?, ?, ?,
		?, ?,
		?, ?, ?, ?
	)`,
		issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design,
		issue.AcceptanceCriteria, issue.Notes,
		string(issue.Status), issue.Priority, string(issue.IssueType), issue.Assignee, issue.EstimatedMinutes,
		formatTime(issue.CreatedAt), issue.CreatedBy, issue.Owner,
		formatTime(issue.UpdatedAt), closedAtStr, issue.ClosedBySession, extRef, issue.SpecID,
		issue.CompactionLevel, compactedAtStr, issue.CompactedAtCommit, issue.OriginalSize, issue.SourceRepo, issue.CloseReason,
		issue.Sender, ephemeralInt, string(issue.WispType), pinnedInt, isTemplateInt, crystallizesInt,
		issue.AwaitType, issue.AwaitID, int64(issue.Timeout), waitersStr,
		issue.HookBead, issue.RoleBead, string(issue.AgentState), lastActivityStr, issue.RoleType, issue.Rig, string(issue.MolType),
		issue.EventKind, issue.Actor, issue.Target, issue.Payload,
		dueAtStr, deferUntilStr,
		qualityScore, string(issue.WorkType), issue.SourceSystem, metadataStr,
	)
	if err != nil {
		return fmt.Errorf("insert ephemeral issue %s: %w", issue.ID, err)
	}

	// Insert labels
	for _, label := range issue.Labels {
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO labels (issue_id, label) VALUES (?, ?)`,
			issue.ID, label); err != nil {
			return fmt.Errorf("insert label for %s: %w", issue.ID, err)
		}
	}

	return nil
}

// GetIssue retrieves an issue by ID.
func (s *Store) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+issueSelectColumns+" FROM issues WHERE id = ?", id)
	issue, err := scanIssue(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ephemeral issue %s: %w", id, err)
	}

	// Load labels
	labels, err := s.getLabels(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	issue.Labels = labels

	return issue, nil
}

type queryContexter interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *Store) getLabels(ctx context.Context, db queryContexter, issueID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT label FROM labels WHERE issue_id = ? ORDER BY label", issueID)
	if err != nil {
		return nil, fmt.Errorf("get labels for %s: %w", issueID, err)
	}
	defer rows.Close()

	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

// GetIssuesByIDs retrieves multiple issues by their IDs.
func (s *Store) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := "SELECT " + issueSelectColumns + " FROM issues WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get ephemeral issues by IDs: %w", err)
	}
	defer rows.Close()

	var issues []*types.Issue
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}

// UpdateIssue updates fields of an existing issue.
func (s *Store) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	if len(updates) == 0 {
		return nil
	}

	var setClauses []string
	var args []any

	for key, val := range updates {
		col := mapFieldToColumn(key)
		if col == "" {
			continue
		}
		setClauses = append(setClauses, col+" = ?")
		args = append(args, normalizeUpdateValue(val))
	}

	if len(setClauses) == 0 {
		return nil
	}

	// Always update updated_at
	setClauses = append(setClauses, "updated_at = ?")
	args = append(args, formatTime(time.Now().UTC()))

	args = append(args, id)
	query := "UPDATE issues SET " + strings.Join(setClauses, ", ") + " WHERE id = ?"

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update ephemeral issue %s: %w", id, err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("ephemeral issue %s not found", id)
	}

	return nil
}

// CloseIssue marks an issue as closed.
func (s *Store) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	now := formatTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx,
		`UPDATE issues SET status = 'closed', closed_at = ?, close_reason = ?, closed_by_session = ?, updated_at = ? WHERE id = ?`,
		now, reason, session, now, id)
	if err != nil {
		return fmt.Errorf("close ephemeral issue %s: %w", id, err)
	}
	return nil
}

// DeleteIssue deletes an issue and its associated data (cascading).
func (s *Store) DeleteIssue(ctx context.Context, id string) error {
	// Foreign keys with ON DELETE CASCADE handle labels, deps, comments, events
	_, err := s.db.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete ephemeral issue %s: %w", id, err)
	}
	return nil
}

// DeleteIssues deletes multiple issues.
func (s *Store) DeleteIssues(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	deleted := 0
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", id)
		if err != nil {
			return deleted, fmt.Errorf("delete ephemeral issue %s: %w", id, err)
		}
		n, _ := result.RowsAffected()
		deleted += int(n)
	}

	return deleted, tx.Commit()
}

// SearchIssues queries issues matching the filter criteria.
func (s *Store) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	var whereClauses []string
	var args []any

	if query != "" {
		whereClauses = append(whereClauses, "(title LIKE ? OR description LIKE ?)")
		q := "%" + query + "%"
		args = append(args, q, q)
	}

	if filter.Status != nil {
		whereClauses = append(whereClauses, "status = ?")
		args = append(args, string(*filter.Status))
	}

	if len(filter.ExcludeStatus) > 0 {
		ph := make([]string, len(filter.ExcludeStatus))
		for i, s := range filter.ExcludeStatus {
			ph[i] = "?"
			args = append(args, string(s))
		}
		whereClauses = append(whereClauses, "status NOT IN ("+strings.Join(ph, ",")+")")
	}

	if filter.IssueType != nil {
		whereClauses = append(whereClauses, "issue_type = ?")
		args = append(args, string(*filter.IssueType))
	}

	if len(filter.ExcludeTypes) > 0 {
		ph := make([]string, len(filter.ExcludeTypes))
		for i, t := range filter.ExcludeTypes {
			ph[i] = "?"
			args = append(args, string(t))
		}
		whereClauses = append(whereClauses, "issue_type NOT IN ("+strings.Join(ph, ",")+")")
	}

	if filter.Priority != nil {
		whereClauses = append(whereClauses, "priority = ?")
		args = append(args, *filter.Priority)
	}

	if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}

	if filter.TitleContains != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		args = append(args, "%"+filter.TitleContains+"%")
	}

	if filter.DescriptionContains != "" {
		whereClauses = append(whereClauses, "description LIKE ?")
		args = append(args, "%"+filter.DescriptionContains+"%")
	}

	if len(filter.IDs) > 0 {
		ph := make([]string, len(filter.IDs))
		for i, id := range filter.IDs {
			ph[i] = "?"
			args = append(args, id)
		}
		whereClauses = append(whereClauses, "id IN ("+strings.Join(ph, ",")+")")
	}

	if filter.WispType != nil {
		whereClauses = append(whereClauses, "wisp_type = ?")
		args = append(args, string(*filter.WispType))
	}

	if filter.MolType != nil {
		whereClauses = append(whereClauses, "mol_type = ?")
		args = append(args, string(*filter.MolType))
	}

	if filter.ParentID != nil {
		whereClauses = append(whereClauses, "id IN (SELECT issue_id FROM dependencies WHERE depends_on_id = ? AND type = 'parent-child')")
		args = append(args, *filter.ParentID)
	}

	if filter.CreatedAfter != nil {
		whereClauses = append(whereClauses, "created_at >= ?")
		args = append(args, formatTime(*filter.CreatedAfter))
	}

	if filter.CreatedBefore != nil {
		whereClauses = append(whereClauses, "created_at <= ?")
		args = append(args, formatTime(*filter.CreatedBefore))
	}

	whereStr := ""
	if len(whereClauses) > 0 {
		whereStr = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	sqlQuery := "SELECT " + issueSelectColumns + " FROM issues" + whereStr + " ORDER BY created_at DESC" + limitSQL

	rows, err := s.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("search ephemeral issues: %w", err)
	}
	defer rows.Close()

	var issues []*types.Issue
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}

// mapFieldToColumn maps update field names to SQL column names.
func mapFieldToColumn(field string) string {
	mapping := map[string]string{
		"title":            "title",
		"description":      "description",
		"design":           "design",
		"notes":            "notes",
		"status":           "status",
		"priority":         "priority",
		"issue_type":       "issue_type",
		"assignee":         "assignee",
		"owner":            "owner",
		"close_reason":     "close_reason",
		"closed_at":        "closed_at",
		"closed_by_session": "closed_by_session",
		"due_at":           "due_at",
		"defer_until":      "defer_until",
		"hook_bead":        "hook_bead",
		"role_bead":        "role_bead",
		"agent_state":      "agent_state",
		"last_activity":    "last_activity",
		"mol_type":         "mol_type",
		"wisp_type":        "wisp_type",
		"metadata":         "metadata",
		"spec_id":          "spec_id",
		"external_ref":     "external_ref",
		"source_repo":      "source_repo",
		"pinned":           "pinned",
		"ephemeral":        "ephemeral",
	}
	return mapping[field]
}

// normalizeUpdateValue converts update values to SQL-compatible types.
func normalizeUpdateValue(val interface{}) interface{} {
	switch v := val.(type) {
	case time.Time:
		return formatTime(v)
	case *time.Time:
		if v == nil {
			return nil
		}
		return formatTime(*v)
	case types.Status:
		return string(v)
	case types.IssueType:
		return string(v)
	case json.RawMessage:
		return string(v)
	case bool:
		if v {
			return 1
		}
		return 0
	default:
		return val
	}
}
