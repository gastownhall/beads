package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// AddComment adds a comment event to an issue
func (s *DoltStore) AddComment(ctx context.Context, issueID, actor, comment string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := issueops.AddCommentEventInTx(ctx, tx, issueID, actor, comment); err != nil {
		return err
	}
	return tx.Commit()
}

// GetEvents retrieves events for an issue
func (s *DoltStore) GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error) {
	table := "events"
	if s.isActiveWisp(ctx, issueID) {
		table = "wisp_events"
	}

	//nolint:gosec // G201: table is hardcoded
	query := fmt.Sprintf(`
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM %s
		WHERE issue_id = ?
		ORDER BY created_at DESC
	`, table)
	args := []interface{}{issueID}

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get events: %w", err)
	}
	defer rows.Close()

	return scanEvents(rows)
}

// GetAllEventsSince returns all events created after the given time, ordered by creation time.
// Queries both events and wisp_events tables. Uses created_at for filtering because
// event IDs are UUIDs (not sequential integers) and cannot be used as high-water marks.
func (s *DoltStore) GetAllEventsSince(ctx context.Context, since time.Time) ([]*types.Event, error) {
	rows, err := s.queryContext(ctx, `
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM events
		WHERE created_at > ?
		UNION ALL
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM wisp_events
		WHERE created_at > ?
		ORDER BY created_at ASC
	`, since, since)
	if err != nil {
		return nil, fmt.Errorf("failed to get events since %v: %w", since, err)
	}
	defer rows.Close()

	return scanEvents(rows)
}

// AddIssueComment adds a comment to an issue (structured comment)
func (s *DoltStore) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	return s.ImportIssueComment(ctx, issueID, author, text, time.Now().UTC())
}

// ImportIssueComment adds a comment during import, preserving the original timestamp.
// This prevents comment timestamp drift across import/export cycles.
func (s *DoltStore) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	var result *types.Comment
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.ImportIssueCommentInTx(ctx, tx, issueID, author, text, createdAt)
		return err
	})
	return result, err
}

// GetIssueComments retrieves all comments for an issue
func (s *DoltStore) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	table := "comments"
	if s.isActiveWisp(ctx, issueID) {
		table = "wisp_comments"
	}

	//nolint:gosec // G201: table is hardcoded
	rows, err := s.queryContext(ctx, fmt.Sprintf(`
		SELECT id, issue_id, author, text, created_at
		FROM %s
		WHERE issue_id = ?
		ORDER BY created_at ASC, id ASC
	`, table), issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get comments: %w", err)
	}
	defer rows.Close()

	return scanComments(rows)
}

// GetCommentsForIssues retrieves comments for multiple issues
func (s *DoltStore) GetCommentsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Comment, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]*types.Comment), nil
	}

	result := make(map[string][]*types.Comment)
	wispIDs, permIDs := s.partitionByWispStatus(ctx, issueIDs)

	// Query permanent comments table
	if len(permIDs) > 0 {
		if err := s.getCommentsForIDsInto(ctx, "comments", permIDs, result); err != nil {
			return nil, err
		}
	}

	// Query wisp_comments table
	if len(wispIDs) > 0 {
		if err := s.getCommentsForIDsInto(ctx, "wisp_comments", wispIDs, result); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// getCommentsForIDsInto queries comments from the specified table and merges into result.
// Uses batched IN clauses (queryBatchSize) to avoid full table scans on Dolt with large ID sets.
func (s *DoltStore) getCommentsForIDsInto(ctx context.Context, table string, ids []string, result map[string][]*types.Comment) error {
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders, args := doltBuildSQLInClause(batch)

		//nolint:gosec // G201: table is hardcoded, placeholders contains only ? markers
		query := fmt.Sprintf(`
			SELECT id, issue_id, author, text, created_at
			FROM %s
			WHERE issue_id IN (%s)
			ORDER BY issue_id, created_at ASC, id ASC
		`, table, placeholders)

		rows, err := s.queryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("failed to get comments from %s: %w", table, err)
		}

		for rows.Next() {
			var c types.Comment
			if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
				_ = rows.Close()
				return fmt.Errorf("failed to scan comment: %w", err)
			}
			result[c.IssueID] = append(result[c.IssueID], &c)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
	}
	return nil
}

// GetCommentCounts returns the number of comments for each issue in a single batch query.
// Delegates to issueops.GetCommentCountsInTx for shared query logic.
func (s *DoltStore) GetCommentCounts(ctx context.Context, issueIDs []string) (map[string]int, error) {
	var result map[string]int
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetCommentCountsInTx(ctx, tx, issueIDs)
		return err
	})
	return result, err
}

// scanEvents scans event rows into a slice.
func scanEvents(rows *sql.Rows) ([]*types.Event, error) {
	var events []*types.Event
	for rows.Next() {
		var event types.Event
		var oldValue, newValue, comment sql.NullString
		if err := rows.Scan(&event.ID, &event.IssueID, &event.EventType, &event.Actor,
			&oldValue, &newValue, &comment, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}
		if oldValue.Valid {
			event.OldValue = &oldValue.String
		}
		if newValue.Valid {
			event.NewValue = &newValue.String
		}
		if comment.Valid {
			event.Comment = &comment.String
		}
		events = append(events, &event)
	}
	return events, rows.Err()
}

// scanComments scans comment rows into a slice.
func scanComments(rows *sql.Rows) ([]*types.Comment, error) {
	var comments []*types.Comment
	for rows.Next() {
		var c types.Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan comment: %w", err)
		}
		comments = append(comments, &c)
	}
	return comments, rows.Err()
}

func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for _, s := range strs[1:] {
		result += sep + s
	}
	return result
}
