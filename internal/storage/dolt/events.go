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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, issueID, types.EventCommented, actor, comment)
	if err != nil {
		return fmt.Errorf("failed to add comment: %w", err)
	}
	return nil
}

// GetEvents retrieves events for an issue
func (s *DoltStore) GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error) {
	query := `
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM events
		WHERE issue_id = ?
		ORDER BY created_at DESC
	`
	args := []interface{}{issueID}

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get events: %w", err)
	}
	defer rows.Close()

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

// AddIssueComment adds a comment to an issue (structured comment)
func (s *DoltStore) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO comments (issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?)
	`, issueID, author, text, time.Now())
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
		CreatedAt: time.Now(),
	}, nil
}

// GetIssueComments retrieves all comments for an issue
func (s *DoltStore) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, issue_id, author, text, created_at
		FROM comments
		WHERE issue_id = ?
		ORDER BY created_at ASC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get comments: %w", err)
	}
	defer rows.Close()

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

// GetCommentsForIssues retrieves comments for multiple issues
func (s *DoltStore) GetCommentsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Comment, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]*types.Comment), nil
	}

	placeholders := make([]string, len(issueIDs))
	args := make([]interface{}, len(issueIDs))
	for i, id := range issueIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// nolint:gosec // G201: placeholders contains only ? markers, actual values passed via args
	query := fmt.Sprintf(`
		SELECT id, issue_id, author, text, created_at
		FROM comments
		WHERE issue_id IN (%s)
		ORDER BY issue_id, created_at ASC
	`, joinStrings(placeholders, ","))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get comments: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]*types.Comment)
	for rows.Next() {
		var c types.Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan comment: %w", err)
		}
		result[c.IssueID] = append(result[c.IssueID], &c)
	}
	return result, rows.Err()
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
