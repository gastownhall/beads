package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// executeLabelOperation executes a label operation (add or remove) within a transaction
func (s *SQLiteStorage) executeLabelOperation(
	ctx context.Context,
	issueID, actor string,
	labelSQL string,
	labelSQLArgs []interface{},
	eventType types.EventType,
	eventComment string,
	operationError string,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, labelSQL, labelSQLArgs...)
	if err != nil {
		return fmt.Errorf("%s: %w", operationError, err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, issueID, eventType, actor, eventComment)
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	// Mark issue as dirty for incremental export
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, issueID, time.Now())
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	return tx.Commit()
}

// AddLabel adds a label to an issue
func (s *SQLiteStorage) AddLabel(ctx context.Context, issueID, label, actor string) error {
	return s.executeLabelOperation(
		ctx, issueID, actor,
		`INSERT OR IGNORE INTO labels (issue_id, label) VALUES (?, ?)`,
		[]interface{}{issueID, label},
		types.EventLabelAdded,
		fmt.Sprintf("Added label: %s", label),
		"failed to add label",
	)
}

// RemoveLabel removes a label from an issue
func (s *SQLiteStorage) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	return s.executeLabelOperation(
		ctx, issueID, actor,
		`DELETE FROM labels WHERE issue_id = ? AND label = ?`,
		[]interface{}{issueID, label},
		types.EventLabelRemoved,
		fmt.Sprintf("Removed label: %s", label),
		"failed to remove label",
	)
}

// GetLabels returns all labels for an issue
func (s *SQLiteStorage) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT label FROM labels WHERE issue_id = ? ORDER BY label
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}

	return labels, nil
}

// GetIssuesByLabel returns issues with a specific label
func (s *SQLiteStorage) GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		       i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		       i.created_at, i.updated_at, i.closed_at, i.external_ref
		FROM issues i
		JOIN labels l ON i.id = l.issue_id
		WHERE l.label = ?
		ORDER BY i.priority ASC, i.created_at DESC
	`, label)
	if err != nil {
		return nil, fmt.Errorf("failed to get issues by label: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return s.scanIssues(ctx, rows)
}
