package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// AddLabel adds a label to an issue
func (s *DoltStore) AddLabel(ctx context.Context, issueID, label, actor string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.AddLabelInTx(ctx, tx, "", "", issueID, label, actor)
	})
}

// RemoveLabel removes a label from an issue.
// Delegates SQL work to issueops.RemoveLabelInTx which handles wisp routing.
func (s *DoltStore) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.RemoveLabelInTx(ctx, tx, "", "", issueID, label, actor)
	})
}

// GetLabels retrieves all labels for an issue
func (s *DoltStore) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	var labels []string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		labels, err = issueops.GetLabelsInTx(ctx, tx, "", issueID)
		return err
	})
	return labels, err
}

// GetLabelsForIssues retrieves labels for multiple issues.
// Delegates to issueops.GetLabelsForIssuesInTx for shared query logic.
func (s *DoltStore) GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	var result map[string][]string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetLabelsForIssuesInTx(ctx, tx, issueIDs)
		return err
	})
	return result, err
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
		return nil, wrapQueryError("iterate issues by label", err)
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
			return nil, wrapDBError("get issue by label", err)
		}
		if issue != nil {
			issues = append(issues, issue)
		}
	}
	return issues, nil
}
