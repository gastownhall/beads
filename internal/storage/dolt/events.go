package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// AddComment adds a comment event to an issue
func (s *DoltStore) AddComment(ctx context.Context, issueID, actor, comment string) error {
	table := wispEventTable(issueID)
	if IsEphemeralID(issueID) && !s.isActiveWisp(ctx, issueID) {
		table = "events" // Promoted wisp — use permanent table
	}

	//nolint:gosec // G201: table is hardcoded
	_, err := s.execContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, table), issueID, types.EventCommented, actor, comment)
	if err != nil {
		return fmt.Errorf("failed to add comment: %w", err)
	}
	return nil
}

// GetEvents retrieves events for an issue
func (s *DoltStore) GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error) {
	table := wispEventTable(issueID)
	if IsEphemeralID(issueID) && !s.isActiveWisp(ctx, issueID) {
		table = "events" // Promoted wisp — use permanent table
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

// GetAllEventsSince returns all events with ID greater than sinceID, ordered by ID ascending.
// Queries both events and wisp_events tables.
func (s *DoltStore) GetAllEventsSince(ctx context.Context, sinceID int64) ([]*types.Event, error) {
	rows, err := s.queryContext(ctx, `
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM events
		WHERE id > ?
		UNION ALL
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM wisp_events
		WHERE id > ?
		ORDER BY id ASC
	`, sinceID, sinceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get events since %d: %w", sinceID, err)
	}
	defer rows.Close()

	return scanEvents(rows)
}

// AddIssueComment adds a comment to an issue (structured comment)
func (s *DoltStore) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	return s.ImportIssueComment(ctx, issueID, author, text, time.Now().UTC())
}

// ImportIssueComment adds a comment during import, preserving the original timestamp.
// This prevents comment timestamp drift across JSONL sync cycles.
func (s *DoltStore) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	// Verify issue exists — route to wisps table for active wisps
	issueTable := wispIssueTable(issueID)
	commentTable := wispCommentTable(issueID)
	if IsEphemeralID(issueID) && !s.isActiveWisp(ctx, issueID) {
		issueTable = "issues"
		commentTable = "comments"
	}

	// Verify issue exists — use queryRowContext for server-mode retry.
	var exists bool
	//nolint:gosec // G201: table is hardcoded
	if err := s.queryRowContext(ctx, func(row *sql.Row) error {
		return row.Scan(&exists)
	}, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE id = ?)`, issueTable), issueID); err != nil {
		return nil, fmt.Errorf("failed to check issue existence: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}

	createdAt = createdAt.UTC()
	//nolint:gosec // G201: table is hardcoded
	result, err := s.execContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?)
	`, commentTable), issueID, author, text, createdAt)
	if err != nil {
		return nil, fmt.Errorf("failed to add comment: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get comment id: %w", err)
	}

	return &types.Comment{
		ID:        id,
		IssueID:   issueID,
		Author:    author,
		Text:      text,
		CreatedAt: createdAt,
	}, nil
}

// GetIssueComments retrieves all comments for an issue
func (s *DoltStore) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	table := wispCommentTable(issueID)
	if IsEphemeralID(issueID) && !s.isActiveWisp(ctx, issueID) {
		table = "comments" // Promoted wisp — use permanent table
	}

	//nolint:gosec // G201: table is hardcoded
	rows, err := s.queryContext(ctx, fmt.Sprintf(`
		SELECT id, issue_id, author, text, created_at
		FROM %s
		WHERE issue_id = ?
		ORDER BY created_at ASC
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
	ephIDs, doltIDs := partitionIDs(issueIDs)

	// Query dolt comments table
	if len(doltIDs) > 0 {
		if err := s.getCommentsForIDsInto(ctx, "comments", doltIDs, result); err != nil {
			return nil, err
		}
	}

	// Query wisp_comments table
	if len(ephIDs) > 0 {
		if err := s.getCommentsForIDsInto(ctx, "wisp_comments", ephIDs, result); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// getCommentsForIDsInto queries comments from the specified table and merges into result.
func (s *DoltStore) getCommentsForIDsInto(ctx context.Context, table string, ids []string, result map[string][]*types.Comment) error {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	//nolint:gosec // G201: table is hardcoded, placeholders contains only ? markers
	query := fmt.Sprintf(`
		SELECT id, issue_id, author, text, created_at
		FROM %s
		WHERE issue_id IN (%s)
		ORDER BY issue_id, created_at ASC
	`, table, joinStrings(placeholders, ","))

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to get comments from %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var c types.Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
			return fmt.Errorf("failed to scan comment: %w", err)
		}
		result[c.IssueID] = append(result[c.IssueID], &c)
	}
	return rows.Err()
}

// GetCommentCounts returns the number of comments for each issue in a single batch query.
func (s *DoltStore) GetCommentCounts(ctx context.Context, issueIDs []string) (map[string]int, error) {
	if len(issueIDs) == 0 {
		return make(map[string]int), nil
	}

	result := make(map[string]int)
	ephIDs, doltIDs := partitionIDs(issueIDs)

	// Query dolt comments table
	if len(doltIDs) > 0 {
		if err := s.getCommentCountsInto(ctx, "comments", doltIDs, result); err != nil {
			return nil, err
		}
	}

	// Query wisp_comments table
	if len(ephIDs) > 0 {
		if err := s.getCommentCountsInto(ctx, "wisp_comments", ephIDs, result); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// getCommentCountsInto queries comment counts from the specified table and merges into result.
func (s *DoltStore) getCommentCountsInto(ctx context.Context, table string, ids []string, result map[string]int) error {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	//nolint:gosec // G201: table is hardcoded, placeholders contains only ? markers
	query := fmt.Sprintf(`
		SELECT issue_id, COUNT(*) as comment_count
		FROM %s
		WHERE issue_id IN (%s)
		GROUP BY issue_id
	`, table, joinStrings(placeholders, ","))

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to get comment counts from %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var issueID string
		var count int
		if err := rows.Scan(&issueID, &count); err != nil {
			return fmt.Errorf("failed to scan comment count: %w", err)
		}
		result[issueID] = count
	}
	return rows.Err()
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
