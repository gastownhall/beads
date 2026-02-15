package sqlite

import (
	"context"
	"database/sql"
	"fmt"
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
	return s.withTx(ctx, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, labelSQL, labelSQLArgs...)
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

		_, err = conn.ExecContext(ctx, `
			INSERT INTO events (issue_id, event_type, actor, comment)
			VALUES (?, ?, ?, ?)
		`, issueID, eventType, actor, eventComment)
		if err != nil {
			return fmt.Errorf("failed to record event: %w", err)
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
// Note: This method is called from GetIssue which already holds reconnectMu.RLock(),
// so we don't acquire the lock here to avoid deadlock. Callers must ensure
// appropriate locking when calling directly.
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
			return nil, wrapDBError("scan label", err)
		}
		labels = append(labels, label)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("iterate labels", err)
	}

	return labels, nil
}

// GetLabelsForIssues fetches labels for multiple issues in a single query
// Returns a map of issue_id -> []labels
func (s *SQLiteStorage) GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]string), nil
	}

	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

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
			return nil, wrapDBError("scan label for issue", err)
		}
		result[issueID] = append(result[issueID], label)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("iterate labels for issues", err)
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
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.content_hash, i.title, i.description, i.design, i.acceptance_criteria, i.notes,
		       i.status, i.priority, i.issue_type, i.assignee, i.estimated_minutes,
		       i.created_at, i.created_by, i.owner, i.updated_at, i.closed_at, i.external_ref, i.spec_id, i.source_repo, i.close_reason,
		       i.sender, i.ephemeral, i.pinned, i.is_template, i.crystallizes,
		       i.await_type, i.await_id, i.timeout_ns, i.waiters,
		       i.hook_bead, i.role_bead, i.agent_state, i.last_activity, i.role_type, i.rig, i.mol_type,
		       i.due_at, i.defer_until, i.metadata
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
