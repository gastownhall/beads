//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func (s *EmbeddedDoltStore) AddInboxItem(ctx context.Context, item *types.InboxItem) error {
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
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
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
				expires_at = VALUES(expires_at),
				rejected_at = NULL,
				rejection_reason = NULL,
				imported_at = NULL,
				imported_issue_id = NULL
		`,
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
		return err
	})
}

func (s *EmbeddedDoltStore) GetInboxItem(ctx context.Context, inboxID string) (*types.InboxItem, error) {
	var item types.InboxItem
	var description, labels, metadata, senderRef sql.NullString
	var importedIssueID, rejectionReason sql.NullString
	var importedAt, rejectedAt, expiresAt sql.NullTime
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT inbox_id, sender_project_id, sender_issue_id, title, description,
				priority, issue_type, status, labels, metadata, sender_ref,
				imported_issue_id, rejection_reason,
				created_at, imported_at, rejected_at, expires_at
			FROM beads_inbox WHERE inbox_id = ?
		`, inboxID).Scan(
			&item.InboxID, &item.SenderProjectID, &item.SenderIssueID,
			&item.Title, &description, &item.Priority, &item.IssueType,
			&item.Status, &labels, &metadata, &senderRef,
			&importedIssueID, &rejectionReason,
			&item.CreatedAt, &importedAt, &rejectedAt, &expiresAt,
		)
	})
	if err != nil {
		return nil, fmt.Errorf("get inbox item: %w", err)
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

func (s *EmbeddedDoltStore) GetInboxItemByPrefix(ctx context.Context, prefix string) (*types.InboxItem, error) {
	var items []*types.InboxItem
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT inbox_id, sender_project_id, sender_issue_id, title, description,
				priority, issue_type, status, labels, metadata, sender_ref,
				imported_issue_id, rejection_reason,
				created_at, imported_at, rejected_at, expires_at
			FROM beads_inbox WHERE inbox_id LIKE ?
			LIMIT 2
		`, prefix+"%")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item types.InboxItem
			var description, labels, metadata, senderRef sql.NullString
			var importedIssueID, rejectionReason sql.NullString
			var importedAt, rejectedAt, expiresAt sql.NullTime
			if err := rows.Scan(
				&item.InboxID, &item.SenderProjectID, &item.SenderIssueID,
				&item.Title, &description, &item.Priority, &item.IssueType,
				&item.Status, &labels, &metadata, &senderRef,
				&importedIssueID, &rejectionReason,
				&item.CreatedAt, &importedAt, &rejectedAt, &expiresAt,
			); err != nil {
				return err
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
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("get inbox item by prefix: %w", err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no inbox item matching prefix %q", prefix)
	}
	if len(items) > 1 {
		return nil, fmt.Errorf("ambiguous prefix %q matches %d items", prefix, len(items))
	}
	return items[0], nil
}

func (s *EmbeddedDoltStore) GetPendingInboxItems(ctx context.Context) ([]*types.InboxItem, error) {
	var items []*types.InboxItem
	now := time.Now().UTC()
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT inbox_id, sender_project_id, sender_issue_id, title, description,
				priority, issue_type, status, labels, metadata, sender_ref,
				imported_issue_id, rejection_reason,
				created_at, imported_at, rejected_at, expires_at
			FROM beads_inbox
			WHERE imported_at IS NULL
			  AND rejected_at IS NULL
			  AND (expires_at IS NULL OR expires_at > ?)
			ORDER BY created_at ASC
		`, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item types.InboxItem
			var description, labels, metadata, senderRef sql.NullString
			var importedIssueID, rejectionReason sql.NullString
			var importedAt, rejectedAt, expiresAt sql.NullTime
			if err := rows.Scan(
				&item.InboxID, &item.SenderProjectID, &item.SenderIssueID,
				&item.Title, &description, &item.Priority, &item.IssueType,
				&item.Status, &labels, &metadata, &senderRef,
				&importedIssueID, &rejectionReason,
				&item.CreatedAt, &importedAt, &rejectedAt, &expiresAt,
			); err != nil {
				return fmt.Errorf("scan inbox item: %w", err)
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
		return rows.Err()
	})
	return items, err
}

func (s *EmbeddedDoltStore) MarkInboxItemImported(ctx context.Context, inboxID string, importedIssueID string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE beads_inbox SET imported_at = ?, imported_issue_id = ? WHERE inbox_id = ?",
			time.Now().UTC(), importedIssueID, inboxID,
		)
		return err
	})
}

func (s *EmbeddedDoltStore) MarkInboxItemRejected(ctx context.Context, inboxID string, reason string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE beads_inbox SET rejected_at = ?, rejection_reason = ? WHERE inbox_id = ?",
			time.Now().UTC(), reason, inboxID,
		)
		return err
	})
}

func (s *EmbeddedDoltStore) CleanInbox(ctx context.Context) (int64, error) {
	var affected int64
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM beads_inbox
			WHERE imported_at IS NOT NULL
			   OR rejected_at IS NOT NULL
			   OR (expires_at IS NOT NULL AND expires_at <= ?)
		`, time.Now().UTC())
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		return err
	})
	return affected, err
}

func (s *EmbeddedDoltStore) CountPendingInbox(ctx context.Context) (int64, error) {
	var count int64
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM beads_inbox
			WHERE imported_at IS NULL
			  AND rejected_at IS NULL
			  AND (expires_at IS NULL OR expires_at > ?)
		`, time.Now().UTC()).Scan(&count)
	})
	return count, err
}
