package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Inbox CRUD operations for cross-project issue delivery.

// AddInboxItem inserts an item into the inbox table.
// Uses ON DUPLICATE KEY UPDATE so resends with updated content succeed.
func (s *DoltStore) AddInboxItem(ctx context.Context, item *types.InboxItem) error {
	if err := storage.ValidateInboxItem(item); err != nil {
		return err
	}
	// Default JSON columns to valid JSON if empty
	labels := item.Labels
	if labels == "" {
		labels = "[]"
	}
	metadata := item.Metadata
	if metadata == "" {
		metadata = "{}"
	}
	query := `
		INSERT INTO beads_inbox (
			inbox_id, sender_project_id, sender_issue_id, title, description,
			priority, issue_type, status, labels, metadata, sender_ref, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			title = VALUES(title),
			description = VALUES(description),
			priority = VALUES(priority),
			issue_type = VALUES(issue_type),
			status = VALUES(status),
			labels = VALUES(labels),
			metadata = VALUES(metadata),
			sender_ref = VALUES(sender_ref),
			expires_at = VALUES(expires_at)
	`
	_, err := s.execContext(ctx, query,
		item.InboxID,
		item.SenderProjectID,
		item.SenderIssueID,
		item.Title,
		item.Description,
		item.Priority,
		item.IssueType,
		item.Status,
		labels,
		metadata,
		item.SenderRef,
		item.ExpiresAt,
	)
	return wrapExecError("add inbox item", err)
}

// GetInboxItem retrieves a single inbox item by ID.
func (s *DoltStore) GetInboxItem(ctx context.Context, inboxID string) (*types.InboxItem, error) {
	query := `
		SELECT inbox_id, sender_project_id, sender_issue_id, title, description,
			priority, issue_type, status, labels, metadata, sender_ref,
			imported_issue_id, rejection_reason,
			created_at, imported_at, rejected_at, expires_at
		FROM beads_inbox WHERE inbox_id = ?
	`
	row := s.db.QueryRowContext(ctx, query, inboxID)
	item, err := scanInboxItem(row)
	if err != nil {
		return nil, wrapQueryError("get inbox item", err)
	}
	return item, nil
}

// GetInboxItemByPrefix retrieves an inbox item by ID prefix.
// Returns an error if the prefix matches zero or more than one item.
func (s *DoltStore) GetInboxItemByPrefix(ctx context.Context, prefix string) (*types.InboxItem, error) {
	query := `
		SELECT inbox_id, sender_project_id, sender_issue_id, title, description,
			priority, issue_type, status, labels, metadata, sender_ref,
			imported_issue_id, rejection_reason,
			created_at, imported_at, rejected_at, expires_at
		FROM beads_inbox WHERE inbox_id LIKE ?
		LIMIT 2
	`
	rows, err := s.db.QueryContext(ctx, query, prefix+"%")
	if err != nil {
		return nil, wrapQueryError("get inbox item by prefix", err)
	}
	defer rows.Close()
	items, err := scanInboxItems(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no inbox item matching prefix %q", prefix)
	}
	if len(items) > 1 {
		return nil, fmt.Errorf("ambiguous prefix %q matches %d items", prefix, len(items))
	}
	return items[0], nil
}

// GetPendingInboxItems returns all items not yet imported or rejected.
func (s *DoltStore) GetPendingInboxItems(ctx context.Context) ([]*types.InboxItem, error) {
	now := time.Now().UTC()
	query := `
		SELECT inbox_id, sender_project_id, sender_issue_id, title, description,
			priority, issue_type, status, labels, metadata, sender_ref,
			imported_issue_id, rejection_reason,
			created_at, imported_at, rejected_at, expires_at
		FROM beads_inbox
		WHERE imported_at IS NULL
		  AND rejected_at IS NULL
		  AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY created_at ASC
	`
	rows, err := s.db.QueryContext(ctx, query, now)
	if err != nil {
		return nil, wrapQueryError("get pending inbox items", err)
	}
	defer rows.Close()
	return scanInboxItems(rows)
}

// MarkInboxItemImported marks an inbox item as imported and records the local issue ID.
func (s *DoltStore) MarkInboxItemImported(ctx context.Context, inboxID string, importedIssueID string) error {
	_, err := s.execContext(ctx,
		"UPDATE beads_inbox SET imported_at = ?, imported_issue_id = ? WHERE inbox_id = ?",
		time.Now().UTC(), importedIssueID, inboxID,
	)
	return wrapExecError("mark inbox item imported", err)
}

// MarkInboxItemRejected marks an inbox item as rejected with a reason.
func (s *DoltStore) MarkInboxItemRejected(ctx context.Context, inboxID string, reason string) error {
	_, err := s.execContext(ctx,
		"UPDATE beads_inbox SET rejected_at = ?, rejection_reason = ? WHERE inbox_id = ?",
		time.Now().UTC(), reason, inboxID,
	)
	return wrapExecError("mark inbox item rejected", err)
}

// CleanInbox removes imported, rejected, and expired inbox items.
// Returns the number of rows removed.
func (s *DoltStore) CleanInbox(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	result, err := s.execContext(ctx, `
		DELETE FROM beads_inbox
		WHERE imported_at IS NOT NULL
		   OR rejected_at IS NOT NULL
		   OR (expires_at IS NOT NULL AND expires_at <= ?)
	`, now)
	if err != nil {
		return 0, wrapExecError("clean inbox", err)
	}
	return result.RowsAffected()
}

// CountPendingInbox returns the count of pending (unimported, unrejected, unexpired) items.
func (s *DoltStore) CountPendingInbox(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	var count int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM beads_inbox
		WHERE imported_at IS NULL
		  AND rejected_at IS NULL
		  AND (expires_at IS NULL OR expires_at > ?)
	`, now).Scan(&count)
	if err != nil {
		return 0, wrapQueryError("count pending inbox", err)
	}
	return count, nil
}

// scanInboxItem scans a single row into an InboxItem.
func scanInboxItem(row *sql.Row) (*types.InboxItem, error) {
	var item types.InboxItem
	var description, labels, metadata, senderRef sql.NullString
	var importedIssueID, rejectionReason sql.NullString
	var importedAt, rejectedAt, expiresAt sql.NullTime
	err := row.Scan(
		&item.InboxID,
		&item.SenderProjectID,
		&item.SenderIssueID,
		&item.Title,
		&description,
		&item.Priority,
		&item.IssueType,
		&item.Status,
		&labels,
		&metadata,
		&senderRef,
		&importedIssueID,
		&rejectionReason,
		&item.CreatedAt,
		&importedAt,
		&rejectedAt,
		&expiresAt,
	)
	if err != nil {
		return nil, err
	}
	item.Description = description.String
	item.Labels = labels.String
	item.Metadata = metadata.String
	item.SenderRef = senderRef.String
	item.ImportedIssueID = importedIssueID.String
	item.RejectionReason = rejectionReason.String
	if importedAt.Valid {
		item.ImportedAt = &importedAt.Time
	}
	if rejectedAt.Valid {
		item.RejectedAt = &rejectedAt.Time
	}
	if expiresAt.Valid {
		item.ExpiresAt = &expiresAt.Time
	}
	return &item, nil
}

// scanInboxItems scans multiple rows into InboxItem slice.
func scanInboxItems(rows *sql.Rows) ([]*types.InboxItem, error) {
	var items []*types.InboxItem
	for rows.Next() {
		var item types.InboxItem
		var description, labels, metadata, senderRef sql.NullString
		var importedIssueID, rejectionReason sql.NullString
		var importedAt, rejectedAt, expiresAt sql.NullTime
		err := rows.Scan(
			&item.InboxID,
			&item.SenderProjectID,
			&item.SenderIssueID,
			&item.Title,
			&description,
			&item.Priority,
			&item.IssueType,
			&item.Status,
			&labels,
			&metadata,
			&senderRef,
			&importedIssueID,
			&rejectionReason,
			&item.CreatedAt,
			&importedAt,
			&rejectedAt,
			&expiresAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan inbox item: %w", err)
		}
		item.Description = description.String
		item.Labels = labels.String
		item.Metadata = metadata.String
		item.SenderRef = senderRef.String
		item.ImportedIssueID = importedIssueID.String
		item.RejectionReason = rejectionReason.String
		if importedAt.Valid {
			item.ImportedAt = &importedAt.Time
		}
		if rejectedAt.Valid {
			item.RejectedAt = &rejectedAt.Time
		}
		if expiresAt.Valid {
			item.ExpiresAt = &expiresAt.Time
		}
		items = append(items, &item)
	}
	return items, rows.Err()
}
