package sqlite

import (
	"context"
	"database/sql"
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
	return s.withTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, labelSQL, labelSQLArgs...)
		if err != nil {
			return fmt.Errorf("%s: %w", operationError, err)
		}

		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to check rows affected: %w", err)
		}
		if rows == 0 {
			// No change made (label already existed or didn't exist), so don't record event
			return nil
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

		return nil
	})
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

// GetLabelsForIssues fetches labels for multiple issues in a single query
// Returns a map of issue_id -> []labels
func (s *SQLiteStorage) GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]string), nil
	}

	// Build placeholders for IN clause
	placeholders := make([]interface{}, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = id
	}

	query := fmt.Sprintf(`
		SELECT issue_id, label 
		FROM labels 
		WHERE issue_id IN (%s)
		ORDER BY issue_id, label
	`, buildPlaceholders(len(issueIDs))) // #nosec G201 -- placeholders are generated internally

	rows, err := s.db.QueryContext(ctx, query, placeholders...)
	if err != nil {
		return nil, fmt.Errorf("failed to batch get labels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make(map[string][]string)
	for rows.Next() {
		var issueID, label string
		if err := rows.Scan(&issueID, &label); err != nil {
			return nil, err
		}
		result[issueID] = append(result[issueID], label)
	}

	return result, nil
}

// buildPlaceholders creates a comma-separated list of SQL placeholders
func buildPlaceholders(count int) string {
	if count == 0 {
		return ""
	}
	result := "?"
	for i := 1; i < count; i++ {
		result += ",?"
	}
	return result
}

// GetIssuesByLabel returns issues with a specific label
func (s *SQLiteStorage) GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.content_hash, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		       i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		       i.created_at, i.created_by, i.updated_at, i.closed_at, i.external_ref, i.source_repo, i.close_reason,
		       i.deleted_at, i.deleted_by, i.delete_reason, i.original_type,
		       i.sender, i.ephemeral, i.pinned, i.is_template,
		       i.await_type, i.await_id, i.timeout_ns, i.waiters
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
