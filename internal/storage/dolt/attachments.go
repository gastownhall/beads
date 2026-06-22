package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// AddAttachment inserts attachment metadata for a persistent issue.
func (s *DoltStore) AddAttachment(ctx context.Context, attachment *types.Attachment) (*types.Attachment, error) {
	var result *types.Attachment
	err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.AddAttachmentInTx(ctx, tx, attachment)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := s.doltAddAndCommit(ctx, []string{"attachments"}, fmt.Sprintf("bd: attach %s", result.IssueID)); err != nil {
		return nil, err
	}
	return result, nil
}

// ListAttachments lists attachment metadata for an issue.
func (s *DoltStore) ListAttachments(ctx context.Context, issueID string) ([]*types.Attachment, error) {
	var result []*types.Attachment
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.ListAttachmentsInTx(ctx, tx, issueID)
		return err
	})
	return result, err
}

// ResolveAttachment resolves an attachment ID, content-hash prefix, or filename.
func (s *DoltStore) ResolveAttachment(ctx context.Context, issueID, selector string) (*types.Attachment, error) {
	var result *types.Attachment
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.ResolveAttachmentInTx(ctx, tx, issueID, selector)
		return err
	})
	return result, err
}

// RemoveAttachment deletes one attachment metadata row.
func (s *DoltStore) RemoveAttachment(ctx context.Context, issueID, attachmentID string) error {
	if err := s.withRetryTx(ctx, func(tx *sql.Tx) error {
		return issueops.RemoveAttachmentInTx(ctx, tx, issueID, attachmentID)
	}); err != nil {
		return err
	}
	return s.doltAddAndCommit(ctx, []string{"attachments"}, fmt.Sprintf("bd: detach %s", issueID))
}
