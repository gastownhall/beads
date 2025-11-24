package sqlite

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// AddIssueComment adds a comment to an issue
func (s *SQLiteStorage) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	// Verify issue exists
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issues WHERE id = ?)`, issueID).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("failed to check issue existence: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}

	// Insert comment
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO comments (issue_id, author, text, created_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	`, issueID, author, text)
	if err != nil {
		return nil, fmt.Errorf("failed to insert comment: %w", err)
	}

	// Get the inserted comment ID
	commentID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get comment ID: %w", err)
	}

	// Fetch the complete comment
	comment := &types.Comment{}
	err = s.db.QueryRowContext(ctx, `
		SELECT id, issue_id, author, text, created_at
		FROM comments WHERE id = ?
	`, commentID).Scan(&comment.ID, &comment.IssueID, &comment.Author, &comment.Text, &comment.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch comment: %w", err)
	}

	// Mark issue as dirty for JSONL export
	if err := s.MarkIssueDirty(ctx, issueID); err != nil {
		return nil, fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	return comment, nil
}

// GetIssueComments retrieves all comments for an issue
func (s *SQLiteStorage) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, issue_id, author, text, created_at
		FROM comments
		WHERE issue_id = ?
		ORDER BY created_at ASC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to query comments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var comments []*types.Comment
	for rows.Next() {
		comment := &types.Comment{}
		err := rows.Scan(&comment.ID, &comment.IssueID, &comment.Author, &comment.Text, &comment.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan comment: %w", err)
		}
		comments = append(comments, comment)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating comments: %w", err)
	}

	return comments, nil
}
