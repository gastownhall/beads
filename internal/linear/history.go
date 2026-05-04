package linear

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
)

// SyncRun represents a single sync invocation persisted to linear_sync_runs.
type SyncRun struct {
	SyncRunID          string    `json:"sync_run_id"`
	StartedAt          time.Time `json:"started_at"`
	CompletedAt        time.Time `json:"completed_at"`
	Direction          string    `json:"direction"`
	DryRun             bool      `json:"dry_run"`
	IssuesCreated      int       `json:"issues_created"`
	IssuesUpdated      int       `json:"issues_updated"`
	IssuesSkipped      int       `json:"issues_skipped"`
	IssuesFailed       int       `json:"issues_failed"`
	IssuesArchived     int       `json:"issues_archived"`
	ConflictResolution string    `json:"conflict_resolution,omitempty"`
	ErrorMessage       string    `json:"error_message,omitempty"`
}

// SyncItem represents a per-issue outcome row in linear_sync_items.
type SyncItem struct {
	ID            string            `json:"id"`
	SyncRunID     string            `json:"sync_run_id"`
	BeadID        string            `json:"bead_id"`
	LinearID      string            `json:"linear_id"`
	Direction     string            `json:"direction"`
	AttemptNumber int               `json:"attempt_number"`
	Outcome       string            `json:"outcome"`
	StatusCode    int               `json:"status_code"`
	DurationMs    int64             `json:"duration_ms"`
	BeforeValues  map[string]string `json:"before_values,omitempty"`
	AfterValues   map[string]string `json:"after_values,omitempty"`
	ErrorMessage  string            `json:"error_message,omitempty"`
}

// SyncHistoryDB provides read/write access to the linear_sync_history tables.
// Requires a *sql.DB because it uses Dolt-specific SQL outside the Storage interface.
type SyncHistoryDB struct {
	db *sql.DB
}

// NewSyncHistoryDB creates a SyncHistoryDB from a raw database connection.
func NewSyncHistoryDB(db *sql.DB) *SyncHistoryDB {
	return &SyncHistoryDB{db: db}
}

// RecordSyncRun writes a complete sync result (run + items) to the history tables.
func (h *SyncHistoryDB) RecordSyncRun(ctx context.Context, run *SyncRun, items []SyncItem) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx,
		`INSERT INTO linear_sync_runs (sync_run_id, started_at, completed_at, direction, dry_run,
			issues_created, issues_updated, issues_skipped, issues_failed, issues_archived,
			conflict_resolution, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.SyncRunID, run.StartedAt, run.CompletedAt, run.Direction, run.DryRun,
		run.IssuesCreated, run.IssuesUpdated, run.IssuesSkipped, run.IssuesFailed, run.IssuesArchived,
		run.ConflictResolution, run.ErrorMessage,
	)
	if err != nil {
		return fmt.Errorf("insert sync run: %w", err)
	}

	if len(items) > 0 {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO linear_sync_items (sync_run_id, bead_id, linear_id, direction,
				attempt_number, outcome, status_code, duration_ms,
				before_values, after_values, error_message)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare item insert: %w", err)
		}
		defer stmt.Close()

		for _, item := range items {
			beforeJSON, _ := marshalFieldValues(item.BeforeValues)
			afterJSON, _ := marshalFieldValues(item.AfterValues)

			_, err := stmt.ExecContext(ctx,
				run.SyncRunID, item.BeadID, item.LinearID, item.Direction,
				item.AttemptNumber, item.Outcome, item.StatusCode, item.DurationMs,
				beforeJSON, afterJSON, item.ErrorMessage,
			)
			if err != nil {
				return fmt.Errorf("insert sync item for bead %s: %w", item.BeadID, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sync history: %w", err)
	}
	return nil
}

// ListSyncRuns returns sync runs matching the filter criteria.
func (h *SyncHistoryDB) ListSyncRuns(ctx context.Context, since *time.Time, limit int) ([]SyncRun, error) {
	query := `SELECT sync_run_id, started_at, completed_at, direction, dry_run,
		issues_created, issues_updated, issues_skipped, issues_failed, issues_archived,
		conflict_resolution, error_message
		FROM linear_sync_runs`
	var args []interface{}
	if since != nil {
		query += " WHERE started_at >= ?"
		args = append(args, *since)
	}
	query += " ORDER BY started_at DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query sync runs: %w", err)
	}
	defer rows.Close()

	var runs []SyncRun
	for rows.Next() {
		var r SyncRun
		if err := rows.Scan(&r.SyncRunID, &r.StartedAt, &r.CompletedAt, &r.Direction, &r.DryRun,
			&r.IssuesCreated, &r.IssuesUpdated, &r.IssuesSkipped, &r.IssuesFailed, &r.IssuesArchived,
			&r.ConflictResolution, &r.ErrorMessage); err != nil {
			return nil, fmt.Errorf("scan sync run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// GetSyncRunItems returns per-issue outcomes for a given sync run.
func (h *SyncHistoryDB) GetSyncRunItems(ctx context.Context, syncRunID string) ([]SyncItem, error) {
	rows, err := h.db.QueryContext(ctx,
		`SELECT id, sync_run_id, bead_id, linear_id, direction,
			attempt_number, outcome, status_code, duration_ms,
			before_values, after_values, error_message
		FROM linear_sync_items
		WHERE sync_run_id = ?
		ORDER BY bead_id`, syncRunID)
	if err != nil {
		return nil, fmt.Errorf("query sync items: %w", err)
	}
	defer rows.Close()

	var items []SyncItem
	for rows.Next() {
		var item SyncItem
		var beforeJSON, afterJSON sql.NullString
		if err := rows.Scan(&item.ID, &item.SyncRunID, &item.BeadID, &item.LinearID,
			&item.Direction, &item.AttemptNumber, &item.Outcome, &item.StatusCode,
			&item.DurationMs, &beforeJSON, &afterJSON, &item.ErrorMessage); err != nil {
			return nil, fmt.Errorf("scan sync item: %w", err)
		}
		if beforeJSON.Valid {
			item.BeforeValues = unmarshalFieldValues(beforeJSON.String)
		}
		if afterJSON.Valid {
			item.AfterValues = unmarshalFieldValues(afterJSON.String)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetSyncRun returns a single sync run by ID.
func (h *SyncHistoryDB) GetSyncRun(ctx context.Context, syncRunID string) (*SyncRun, error) {
	var r SyncRun
	err := h.db.QueryRowContext(ctx,
		`SELECT sync_run_id, started_at, completed_at, direction, dry_run,
			issues_created, issues_updated, issues_skipped, issues_failed, issues_archived,
			conflict_resolution, error_message
		FROM linear_sync_runs WHERE sync_run_id = ?`, syncRunID).
		Scan(&r.SyncRunID, &r.StartedAt, &r.CompletedAt, &r.Direction, &r.DryRun,
			&r.IssuesCreated, &r.IssuesUpdated, &r.IssuesSkipped, &r.IssuesFailed, &r.IssuesArchived,
			&r.ConflictResolution, &r.ErrorMessage)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get sync run %s: %w", syncRunID, err)
	}
	return &r, nil
}

// BuildSyncRunFromResult creates a SyncRun from a tracker.SyncResult plus
// additional metadata collected during the sync invocation.
func BuildSyncRunFromResult(runID string, started time.Time, direction string, dryRun bool,
	conflictRes string, result *tracker.SyncResult) *SyncRun {
	run := &SyncRun{
		SyncRunID:          runID,
		StartedAt:          started,
		CompletedAt:        time.Now().UTC(),
		Direction:          direction,
		DryRun:             dryRun,
		ConflictResolution: conflictRes,
	}
	if result != nil {
		run.IssuesCreated = result.Stats.Created
		run.IssuesUpdated = result.Stats.Updated
		run.IssuesSkipped = result.Stats.Skipped
		run.IssuesFailed = result.Stats.Errors
		if result.Error != "" {
			run.ErrorMessage = result.Error
		}
	}
	return run
}

// BuildSyncItemsFromResult extracts per-issue SyncItem records from a SyncResult.
func BuildSyncItemsFromResult(result *tracker.SyncResult) []SyncItem {
	if result == nil {
		return nil
	}
	var items []SyncItem

	for _, detail := range result.PullStats.Items {
		items = append(items, syncItemFromDetail(detail))
	}
	for _, detail := range result.PushStats.Items {
		items = append(items, syncItemFromDetail(detail))
	}
	return items
}

func syncItemFromDetail(d tracker.SyncItemDetail) SyncItem {
	return SyncItem{
		BeadID:        d.BeadID,
		LinearID:      d.ExternalID,
		Direction:     d.Direction,
		AttemptNumber: 1,
		Outcome:       d.Outcome,
		StatusCode:    d.StatusCode,
		DurationMs:    d.DurationMs,
		BeforeValues:  d.BeforeValues,
		AfterValues:   d.AfterValues,
		ErrorMessage:  d.ErrorMsg,
	}
}

func marshalFieldValues(vals map[string]string) (string, error) {
	if len(vals) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(vals)
	if err != nil {
		return "{}", err
	}
	return string(b), nil
}

func unmarshalFieldValues(s string) map[string]string {
	if s == "" || s == "{}" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}
