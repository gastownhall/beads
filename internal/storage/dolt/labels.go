package dolt

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// AddLabel adds a label to an issue
func (s *DoltStore) AddLabel(ctx context.Context, issueID, label, actor string) error {
	if IsEphemeralID(issueID) {
		return s.addWispLabel(ctx, issueID, label, actor)
	}
	_, err := s.execContext(ctx, `
		INSERT IGNORE INTO labels (issue_id, label) VALUES (?, ?)
	`, issueID, label)
	if err != nil {
		return fmt.Errorf("failed to add label: %w", err)
	}
	comment := "Added label: " + label
	_, err = s.execContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, issueID, types.EventLabelAdded, actor, comment)
	if err != nil {
		return fmt.Errorf("failed to record label event: %w", err)
	}
	return nil
}

// RemoveLabel removes a label from an issue
func (s *DoltStore) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	if IsEphemeralID(issueID) {
		return s.removeWispLabel(ctx, issueID, label)
	}
	_, err := s.execContext(ctx, `
		DELETE FROM labels WHERE issue_id = ? AND label = ?
	`, issueID, label)
	if err != nil {
		return fmt.Errorf("failed to remove label: %w", err)
	}
	comment := "Removed label: " + label
	_, err = s.execContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, issueID, types.EventLabelRemoved, actor, comment)
	if err != nil {
		return fmt.Errorf("failed to record label event: %w", err)
	}
	return nil
}

// GetLabels retrieves all labels for an issue
func (s *DoltStore) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	if IsEphemeralID(issueID) {
		return s.getWispLabels(ctx, issueID)
	}
	rows, err := s.queryContext(ctx, `
		SELECT label FROM labels WHERE issue_id = ? ORDER BY label
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}
	defer rows.Close()

	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, fmt.Errorf("failed to scan label: %w", err)
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

// GetLabelsForIssues retrieves labels for multiple issues
func (s *DoltStore) GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]string), nil
	}

	// Partition into wisp and dolt IDs
	ephIDs, doltIDs := partitionIDs(issueIDs)

	result := make(map[string][]string)

	// Fetch wisp labels
	for _, id := range ephIDs {
		labels, err := s.getWispLabels(ctx, id)
		if err != nil {
			return nil, err
		}
		if len(labels) > 0 {
			result[id] = labels
		}
	}

	// Fetch dolt labels
	if len(doltIDs) > 0 {
		placeholders := make([]string, len(doltIDs))
		args := make([]interface{}, len(doltIDs))
		for i, id := range doltIDs {
			placeholders[i] = "?"
			args[i] = id
		}

		// nolint:gosec // G201: placeholders contains only ? markers, actual values passed via args
		query := fmt.Sprintf(`
			SELECT issue_id, label FROM labels
			WHERE issue_id IN (%s)
			ORDER BY issue_id, label
		`, strings.Join(placeholders, ","))

		rows, err := s.queryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("failed to get labels for issues: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var issueID, label string
			if err := rows.Scan(&issueID, &label); err != nil {
				return nil, fmt.Errorf("failed to scan label: %w", err)
			}
			result[issueID] = append(result[issueID], label)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// GetIssuesByLabel retrieves all issues with a specific label
func (s *DoltStore) GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error) {
	rows, err := s.queryContext(ctx, `
		SELECT i.id FROM issues i
		JOIN labels l ON i.id = l.issue_id
		WHERE l.label = ?
		ORDER BY i.priority ASC, i.created_at DESC
	`, label)
	if err != nil {
		return nil, fmt.Errorf("failed to get issues by label: %w", err)
	}

	// Collect IDs first, then close rows before fetching full issues.
	// This avoids connection pool deadlock when MaxOpenConns=1 (embedded dolt).
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close() // Best effort cleanup on error path
			return nil, fmt.Errorf("failed to scan issue id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close() // Best effort cleanup on error path
		return nil, err
	}
	_ = rows.Close() // Redundant close for safety (rows already iterated)

	// Also check wisp_labels for the same label
	wispRows, err := s.queryContext(ctx, `
		SELECT wl.issue_id FROM wisp_labels wl
		WHERE wl.label = ?
	`, label)
	if err == nil {
		for wispRows.Next() {
			var id string
			if err := wispRows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
		_ = wispRows.Close()
	}

	var issues []*types.Issue
	for _, id := range ids {
		issue, err := s.GetIssue(ctx, id)
		if err != nil {
			return nil, err
		}
		if issue != nil {
			issues = append(issues, issue)
		}
	}
	return issues, nil
}
