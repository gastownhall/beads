package sqlkit

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// AddIssueComment adds a structured comment to an issue, stamped with the
// current UTC time. issueops verifies the issue exists and wisp-routes.
func (s *Store) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	var result *types.Comment
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		var e error
		result, e = issueops.AddIssueCommentInTx(ctx, tx, issueID, author, text)
		return e
	})
	return result, err
}

// GetIssueComments returns all comments on an issue, oldest first.
func (s *Store) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	var result []*types.Comment
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		result, e = issueops.GetIssueCommentsInTx(ctx, tx, issueID)
		return e
	})
	return result, err
}

// GetEvents returns the audit-trail events for an issue (limit <= 0 = all).
func (s *Store) GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error) {
	var result []*types.Event
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		result, e = issueops.GetEventsInTx(ctx, tx, issueID, limit)
		return e
	})
	return result, err
}

// CountIssueComments returns the number of comments on an issue. Intentionally
// not wisp-routed — mirrors the dolt body for oracle parity.
func (s *Store) CountIssueComments(ctx context.Context, issueID string) (int64, error) {
	var n int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			"SELECT count(*) FROM comments WHERE issue_id = ?", issueID).Scan(&n)
	})
	return n, err
}

// CountEvents returns the number of audit events for an issue, capped at limit
// (or unbounded if limit == 0). Not wisp-routed, matching the dolt body.
func (s *Store) CountEvents(ctx context.Context, issueID string, limit int) (int64, error) {
	var n int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			"SELECT count(*) FROM events WHERE issue_id = ?", issueID).Scan(&n)
	})
	if err != nil {
		return 0, err
	}
	if limit > 0 && n > int64(limit) {
		n = int64(limit)
	}
	return n, nil
}

// SetLocalMetadata writes a clone-local (dolt-ignored) metadata value.
func (s *Store) SetLocalMetadata(ctx context.Context, key, value string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.SetLocalMetadataInTx(ctx, tx, key, value)
	})
}

// GetLocalMetadata reads a clone-local metadata value ("" when absent).
func (s *Store) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	var v string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		v, e = issueops.GetLocalMetadataInTx(ctx, tx, key)
		return e
	})
	return v, err
}
