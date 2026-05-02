package tracker

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SyncHistoryEntry represents one completed sync run in the audit log.
type SyncHistoryEntry struct {
	SyncRunID     string    `json:"sync_run_id"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at"`
	Tracker       string    `json:"tracker"`
	Direction     string    `json:"direction"`
	DryRun        bool      `json:"dry_run"`
	IssuesCreated int       `json:"issues_created"`
	IssuesUpdated int       `json:"issues_updated"`
	IssuesSkipped int       `json:"issues_skipped"`
	IssuesFailed  int       `json:"issues_failed"`
	Conflicts     int       `json:"conflicts"`
	Success       bool      `json:"success"`
	ErrorMessage  string    `json:"error_message,omitempty"`
	Actor         string    `json:"actor,omitempty"`
}

// SyncHistoryItem represents one per-issue outcome within a sync run.
type SyncHistoryItem struct {
	SyncRunID    string `json:"sync_run_id"`
	BeadID       string `json:"bead_id"`
	ExternalID   string `json:"external_id,omitempty"`
	Outcome      string `json:"outcome"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// syncDirection returns "pull", "push", or "both" based on SyncOptions.
func syncDirection(opts SyncOptions) string {
	pull := opts.Pull
	push := opts.Push
	if !pull && !push {
		return "both"
	}
	if pull && push {
		return "both"
	}
	if pull {
		return "pull"
	}
	return "push"
}

// RecordSyncHistory persists a sync run to the sync_history table.
// Requires a store that exposes a *sql.DB via the dbProvider interface.
func RecordSyncHistory(ctx context.Context, db *sql.DB, entry *SyncHistoryEntry) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO sync_history (
			sync_run_id, started_at, completed_at, tracker, direction,
			dry_run, issues_created, issues_updated, issues_skipped,
			issues_failed, conflicts, success, error_message, actor
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.SyncRunID,
		entry.StartedAt.UTC(),
		entry.CompletedAt.UTC(),
		entry.Tracker,
		entry.Direction,
		entry.DryRun,
		entry.IssuesCreated,
		entry.IssuesUpdated,
		entry.IssuesSkipped,
		entry.IssuesFailed,
		entry.Conflicts,
		entry.Success,
		nullableString(entry.ErrorMessage),
		entry.Actor,
	)
	return err
}

// RecordSyncHistoryItems persists per-issue outcomes to the sync_history_items table.
func RecordSyncHistoryItems(ctx context.Context, db *sql.DB, items []SyncHistoryItem) error {
	if len(items) == 0 {
		return nil
	}
	for _, item := range items {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO sync_history_items (
				sync_run_id, bead_id, external_id, outcome, error_message
			) VALUES (?, ?, ?, ?, ?)`,
			item.SyncRunID,
			item.BeadID,
			item.ExternalID,
			item.Outcome,
			nullableString(item.ErrorMessage),
		); err != nil {
			return fmt.Errorf("recording sync history item for %s: %w", item.BeadID, err)
		}
	}
	return nil
}

// QuerySyncHistory returns recent sync history entries, optionally filtered by
// tracker name and a minimum start time.
func QuerySyncHistory(ctx context.Context, db *sql.DB, tracker string, since *time.Time, limit int) ([]SyncHistoryEntry, error) {
	query := `SELECT sync_run_id, started_at, completed_at, tracker, direction,
		dry_run, issues_created, issues_updated, issues_skipped,
		issues_failed, conflicts, success, error_message, actor
		FROM sync_history WHERE 1=1`
	var args []interface{}

	if tracker != "" {
		query += " AND tracker = ?"
		args = append(args, tracker)
	}
	if since != nil {
		query += " AND started_at >= ?"
		args = append(args, since.UTC())
	}
	query += " ORDER BY started_at DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying sync history: %w", err)
	}
	defer rows.Close()

	var entries []SyncHistoryEntry
	for rows.Next() {
		var e SyncHistoryEntry
		var errMsg sql.NullString
		if err := rows.Scan(
			&e.SyncRunID, &e.StartedAt, &e.CompletedAt, &e.Tracker,
			&e.Direction, &e.DryRun, &e.IssuesCreated, &e.IssuesUpdated,
			&e.IssuesSkipped, &e.IssuesFailed, &e.Conflicts, &e.Success,
			&errMsg, &e.Actor,
		); err != nil {
			return nil, fmt.Errorf("scanning sync history row: %w", err)
		}
		if errMsg.Valid {
			e.ErrorMessage = errMsg.String
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// QuerySyncHistoryItems returns per-issue outcomes for a specific sync run.
func QuerySyncHistoryItems(ctx context.Context, db *sql.DB, syncRunID string) ([]SyncHistoryItem, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT sync_run_id, bead_id, external_id, outcome, error_message
		FROM sync_history_items
		WHERE sync_run_id = ?
		ORDER BY bead_id`, syncRunID)
	if err != nil {
		return nil, fmt.Errorf("querying sync history items: %w", err)
	}
	defer rows.Close()

	var items []SyncHistoryItem
	for rows.Next() {
		var item SyncHistoryItem
		var extID, errMsg sql.NullString
		if err := rows.Scan(&item.SyncRunID, &item.BeadID, &extID, &item.Outcome, &errMsg); err != nil {
			return nil, fmt.Errorf("scanning sync history item: %w", err)
		}
		if extID.Valid {
			item.ExternalID = extID.String
		}
		if errMsg.Valid {
			item.ErrorMessage = errMsg.String
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// NewSyncRunID generates a new UUID for a sync run.
func NewSyncRunID() string {
	return uuid.New().String()
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
