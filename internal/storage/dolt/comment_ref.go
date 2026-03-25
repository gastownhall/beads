package dolt

import (
	"context"
	"database/sql"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// GetCommentByExternalRef retrieves a comment by its external_ref.
// Implements storage.CommentRefStore.
func (s *DoltStore) GetCommentByExternalRef(ctx context.Context, issueID, externalRef string) (*types.Comment, error) {
	var result *types.Comment
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetCommentByExternalRefInTx(ctx, tx, issueID, externalRef)
		return err
	})
	return result, err
}

// ImportCommentWithRef adds a comment preserving its external_ref and original timestamp.
// Implements storage.CommentRefStore.
func (s *DoltStore) ImportCommentWithRef(ctx context.Context, issueID, author, text, externalRef string, createdAt time.Time) (*types.Comment, error) {
	var result *types.Comment
	err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.ImportIssueCommentWithRefInTx(ctx, tx, issueID, author, text, externalRef, createdAt)
		return err
	})
	return result, err
}

// UpdateCommentExternalRef sets the external_ref on an existing comment.
// Implements storage.CommentRefStore.
func (s *DoltStore) UpdateCommentExternalRef(ctx context.Context, issueID, commentID, externalRef string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.UpdateCommentExternalRefInTx(ctx, tx, issueID, commentID, externalRef)
	})
}
