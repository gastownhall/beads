package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
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

// validDBName matches safe Dolt database identifiers.
var validDBName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_\-]*$`)

// SendToInbox delivers inbox items to a target project's database via cross-database INSERT.
// Requires shared-server mode where both databases are accessible on the same Dolt SQL server.
func (s *DoltStore) SendToInbox(ctx context.Context, target string, items []*types.InboxItem) (int, error) {
	if !validDBName.MatchString(target) {
		return 0, fmt.Errorf("invalid target database name %q: must match [a-zA-Z0-9_-]", target)
	}

	for _, item := range items {
		if err := storage.ValidateInboxItem(item); err != nil {
			return 0, err
		}
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Close()

	// Remember and restore original database on all exit paths
	origDB := s.database
	if !validDBName.MatchString(origDB) {
		return 0, fmt.Errorf("invalid current database name %q", origDB)
	}

	if _, err := conn.ExecContext(ctx, "USE `"+target+"`"); err != nil {
		return 0, fmt.Errorf("cannot access target database %q: %w (is the Dolt server shared?)", target, err)
	}
	defer func() {
		_, _ = conn.ExecContext(ctx, "USE `"+origDB+"`")
	}()

	sent := 0
	for _, item := range items {
		labels := item.Labels
		if labels == "" {
			labels = "[]"
		}
		metadata := item.Metadata
		if metadata == "" {
			metadata = "{}"
		}

		_, err = conn.ExecContext(ctx, `
			INSERT INTO beads_inbox (
				inbox_id, sender_project_id, sender_issue_id, title, description,
				priority, issue_type, status, labels, metadata, sender_ref
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE
				title = VALUES(title),
				description = VALUES(description),
				priority = VALUES(priority),
				issue_type = VALUES(issue_type),
				status = VALUES(status),
				labels = VALUES(labels),
				metadata = VALUES(metadata),
				sender_ref = VALUES(sender_ref)
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
		)
		if err != nil {
			return sent, fmt.Errorf("failed to send %s: %w", item.SenderIssueID, err)
		}
		sent++
	}

	if sent > 0 {
		_, err = conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)",
			fmt.Sprintf("inbox: received %d issue(s) from %s", sent, items[0].SenderProjectID))
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: auto-commit in target failed: %v\n", err)
		}
	}

	return sent, nil
}
