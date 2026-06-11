package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// AddAttachmentInTx inserts attachment metadata for a persistent issue.
// Attachment bytes live outside SQL storage; this only records where the
// byte-store layer placed the object.
func AddAttachmentInTx(ctx context.Context, tx *sql.Tx, attachment *types.Attachment) (*types.Attachment, error) {
	if attachment == nil {
		return nil, fmt.Errorf("attachment is nil")
	}

	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM issues WHERE id = ?)`, attachment.IssueID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check issue existence: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("issue %s: %w", attachment.IssueID, storage.ErrNotFound)
	}

	result := *attachment
	if result.ID == "" {
		result.ID = uuid.Must(uuid.NewV7()).String()
	}
	if result.HashAlgorithm == "" {
		result.HashAlgorithm = "sha256"
	}
	if result.CreatedAt.IsZero() {
		result.CreatedAt = time.Now().UTC()
	} else {
		result.CreatedAt = result.CreatedAt.UTC()
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO attachments (
			id, issue_id, hash_algorithm, content_hash, original_filename,
			mime_type, byte_size, storage_relpath, created_by, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, result.ID, result.IssueID, result.HashAlgorithm, result.ContentHash, result.OriginalFilename,
		result.MimeType, result.ByteSize, result.StorageRelPath, result.CreatedBy, result.CreatedAt); err != nil {
		return nil, fmt.Errorf("add attachment metadata: %w", err)
	}

	return &result, nil
}

// ListAttachmentsInTx lists attachment metadata for an issue.
func ListAttachmentsInTx(ctx context.Context, tx *sql.Tx, issueID string) ([]*types.Attachment, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, issue_id, hash_algorithm, content_hash, original_filename,
		       mime_type, byte_size, storage_relpath, created_by, created_at
		FROM attachments
		WHERE issue_id = ?
		ORDER BY created_at ASC, id ASC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	defer rows.Close()
	return scanAttachments(rows)
}

// ResolveAttachmentInTx resolves a user-facing selector to one attachment row.
// It accepts an attachment ID, content-hash prefix, or original filename.
func ResolveAttachmentInTx(ctx context.Context, tx *sql.Tx, issueID, selector string) (*types.Attachment, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, fmt.Errorf("attachment selector is empty")
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, issue_id, hash_algorithm, content_hash, original_filename,
		       mime_type, byte_size, storage_relpath, created_by, created_at
		FROM attachments
		WHERE issue_id = ?
		  AND (id = ? OR content_hash LIKE ? OR original_filename = ?)
		ORDER BY created_at ASC, id ASC
	`, issueID, selector, selector+"%", selector)
	if err != nil {
		return nil, fmt.Errorf("resolve attachment: %w", err)
	}
	defer rows.Close()

	matches, err := scanAttachments(rows)
	if err != nil {
		return nil, err
	}
	matches = dedupeAttachments(matches)
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("attachment %q on issue %s: %w", selector, issueID, storage.ErrNotFound)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("attachment selector %q on issue %s: %w", selector, issueID, storage.ErrAmbiguous)
	}
}

// RemoveAttachmentInTx deletes one attachment metadata row.
func RemoveAttachmentInTx(ctx context.Context, tx *sql.Tx, issueID, attachmentID string) error {
	res, err := tx.ExecContext(ctx,
		`DELETE FROM attachments WHERE issue_id = ? AND id = ?`, issueID, attachmentID)
	if err != nil {
		return fmt.Errorf("remove attachment metadata: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove attachment metadata rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("attachment %s on issue %s: %w", attachmentID, issueID, storage.ErrNotFound)
	}
	return nil
}

// CountAttachmentsInTx counts attachment metadata rows for an issue.
func CountAttachmentsInTx(ctx context.Context, tx *sql.Tx, issueID string) (int64, error) {
	var n int64
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM attachments WHERE issue_id = ?`, issueID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count attachments: %w", err)
	}
	return n, nil
}

func scanAttachments(rows *sql.Rows) ([]*types.Attachment, error) {
	var attachments []*types.Attachment
	for rows.Next() {
		var a types.Attachment
		if err := rows.Scan(&a.ID, &a.IssueID, &a.HashAlgorithm, &a.ContentHash, &a.OriginalFilename,
			&a.MimeType, &a.ByteSize, &a.StorageRelPath, &a.CreatedBy, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan attachment: %w", err)
		}
		attachments = append(attachments, &a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan attachments: %w", err)
	}
	return attachments, nil
}

func dedupeAttachments(in []*types.Attachment) []*types.Attachment {
	out := make([]*types.Attachment, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, a := range in {
		if a == nil {
			continue
		}
		if _, ok := seen[a.ID]; ok {
			continue
		}
		seen[a.ID] = struct{}{}
		out = append(out, a)
	}
	return out
}
