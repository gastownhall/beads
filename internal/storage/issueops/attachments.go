package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"github.com/steveyegge/beads/internal/types"
)

// CreateAttachmentInTx creates an attachment record within a transaction.
//
//nolint:gosec // G201: table name is hardcoded
func CreateAttachmentInTx(ctx context.Context, tx *sql.Tx, att *types.Attachment) (*types.Attachment, error) {
	issueTable := "issues"
	if IsActiveWispInTx(ctx, tx, att.IssueID) {
		issueTable, _, _, _ = WispTableRouting(true)
	}

	// Verify issue exists.
	var exists bool
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT EXISTS(SELECT 1 FROM %s WHERE id = ?)`, issueTable), att.IssueID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check issue existence: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("issue %s not found", att.IssueID)
	}

	id := uuid.Must(uuid.NewV7()).String()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO attachments (id, issue_id, external_ref, filename, url, mime_type, size_bytes, source, creator)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, att.IssueID, att.ExternalRef, att.Filename, att.URL, att.MimeType, att.SizeBytes, att.Source, att.Creator); err != nil {
		return nil, fmt.Errorf("create attachment: %w", err)
	}

	att.ID = id
	return att, nil
}

// GetIssueAttachmentsInTx retrieves all attachments for an issue within a transaction.
func GetIssueAttachmentsInTx(ctx context.Context, tx *sql.Tx, issueID string) ([]*types.Attachment, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, issue_id, external_ref, filename, url, mime_type, size_bytes, source, creator, created_at
		FROM attachments
		WHERE issue_id = ?
		ORDER BY created_at ASC
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("get issue attachments: %w", err)
	}
	defer rows.Close()

	var attachments []*types.Attachment
	for rows.Next() {
		var a types.Attachment
		if err := rows.Scan(&a.ID, &a.IssueID, &a.ExternalRef, &a.Filename, &a.URL, &a.MimeType, &a.SizeBytes, &a.Source, &a.Creator, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("get issue attachments: scan: %w", err)
		}
		attachments = append(attachments, &a)
	}
	return attachments, rows.Err()
}

// GetAttachmentByExternalRefInTx retrieves an attachment by its external_ref within a transaction.
// Returns nil, nil if not found.
func GetAttachmentByExternalRefInTx(ctx context.Context, tx *sql.Tx, issueID, externalRef string) (*types.Attachment, error) {
	if externalRef == "" {
		return nil, nil
	}

	var a types.Attachment
	err := tx.QueryRowContext(ctx, `
		SELECT id, issue_id, external_ref, filename, url, mime_type, size_bytes, source, creator, created_at
		FROM attachments
		WHERE issue_id = ? AND external_ref = ?
	`, issueID, externalRef).Scan(&a.ID, &a.IssueID, &a.ExternalRef, &a.Filename, &a.URL, &a.MimeType, &a.SizeBytes, &a.Source, &a.Creator, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get attachment by external_ref: %w", err)
	}
	return &a, nil
}
