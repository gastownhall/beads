//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// AddAttachment inserts attachment metadata for a persistent issue.
func (s *EmbeddedDoltStore) AddAttachment(ctx context.Context, attachment *types.Attachment) (*types.Attachment, error) {
	var result *types.Attachment
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.AddAttachmentInTx(ctx, tx, attachment)
		return err
	})
	return result, err
}

// ListAttachments lists attachment metadata for an issue.
func (s *EmbeddedDoltStore) ListAttachments(ctx context.Context, issueID string) ([]*types.Attachment, error) {
	var result []*types.Attachment
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.ListAttachmentsInTx(ctx, tx, issueID)
		return err
	})
	return result, err
}

// ResolveAttachment resolves an attachment ID, content-hash prefix, or filename.
func (s *EmbeddedDoltStore) ResolveAttachment(ctx context.Context, issueID, selector string) (*types.Attachment, error) {
	var result *types.Attachment
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.ResolveAttachmentInTx(ctx, tx, issueID, selector)
		return err
	})
	return result, err
}

// RemoveAttachment deletes one attachment metadata row.
func (s *EmbeddedDoltStore) RemoveAttachment(ctx context.Context, issueID, attachmentID string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.RemoveAttachmentInTx(ctx, tx, issueID, attachmentID)
	})
}
