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
	var result []*types.Event
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetEventsInTx(ctx, tx, issueID, limit)
		return err
	})
	return result, err
}

// GetAllEventsSince returns all events created after the given time, ordered by creation time.
// Queries both events and wisp_events tables.
func (s *DoltStore) GetAllEventsSince(ctx context.Context, since time.Time) ([]*types.Event, error) {
	var result []*types.Event
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetAllEventsSinceInTx(ctx, tx, since)
		return err
	})
	return result, err
}

// AddIssueComment adds a comment to an issue (structured comment)
func (s *DoltStore) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	return s.ImportIssueComment(ctx, issueID, author, text, time.Now().UTC())
}

// ImportIssueComment adds a comment during import, preserving the original timestamp.
// This prevents comment timestamp drift across import/export cycles.
func (s *DoltStore) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	var result *types.Comment
	err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
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
	var result map[string][]*types.Comment
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetCommentsForIssuesInTx(ctx, tx, issueIDs)
		return err
	})
	return result, err
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
