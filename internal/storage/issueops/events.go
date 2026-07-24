package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// GetEventsInTx retrieves events for an issue. If limit <= 0, all events are returned.
//
//nolint:gosec // G201: table is hardcoded via WispTableRouting
func GetEventsInTx(ctx context.Context, tx DBTX, issueID string, limit int) ([]*types.Event, error) {
	_, _, eventTable, _ := WispTableRouting(IsActiveWispInTx(ctx, tx, issueID))

	query := fmt.Sprintf(`
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM %s
		WHERE issue_id = ?
		ORDER BY created_at DESC
	`, eventTable)
	args := []interface{}{issueID}

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get events: %w", err)
	}
	defer rows.Close()

	return scanEvents(rows)
}

// GetAllEventsSinceInTx returns all events created after the given time,
// querying both events and wisp_events tables.
func GetAllEventsSinceInTx(ctx context.Context, tx *sql.Tx, since time.Time) ([]*types.Event, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM events
		WHERE created_at > ?
		UNION ALL
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM wisp_events
		WHERE created_at > ?
		ORDER BY created_at ASC
	`, since, since)
	if err != nil {
		return nil, fmt.Errorf("get events since %v: %w", since, err)
	}
	defer rows.Close()

	return scanEvents(rows)
}

// Event keyset-read bounds. The API's change feed pages the durable events
// table with a bounded limit (default when the caller passes <= 0, hard cap
// otherwise).
const (
	defaultEventsSinceLimit = 100
	maxEventsSinceLimit     = 500
)

// EventsSinceInTx returns durable events strictly after the (createdAt, id)
// keyset cursor, ordered by (created_at ASC, id ASC) and bounded by limit.
// issueID != "" scopes the feed to one bead's history; "" returns all issues'.
//
// The cursor predicate is written as
//
//	created_at >= ? AND ((created_at > ?) OR (id > ?))
//
// which is logically the keyset "(created_at, id) > (cursor)" but SARGABLE: the
// redundant `created_at >= ?` lower bound lets the planner seek
// idx_events_created_at (an IndexedTableAccess range) instead of full-scanning
// and filtering — the bare OR form plans as Table+Filter+TopN on Dolt. The
// nested OR then re-applies strict exclusion (drop the cursor row) and the
// same-second id tie-break. All three placeholders bind the cursor: the time
// twice, then the id. The zero cursor (zero time, empty id) starts from epoch.
//
// With issueID set an additional `issue_id = ?` is ANDed in. There is no
// composite (issue_id, created_at, id) index, so that arm is a single-column
// index seek (idx_events_issue) plus a filter; acceptable for a per-bead drawer,
// which is small.
//
// Scope is the durable `events` table only — wisp_events are deliberately not
// unioned in, unlike GetAllEventsSince, so the feed stays durable-only.
func EventsSinceInTx(ctx context.Context, tx DBTX, cursorCreatedAt time.Time, cursorID, issueID string, limit int) ([]*types.Event, error) {
	if limit <= 0 {
		limit = defaultEventsSinceLimit
	}
	if limit > maxEventsSinceLimit {
		limit = maxEventsSinceLimit
	}

	query := EventsSinceQuery(issueID, limit)
	args := []any{cursorCreatedAt, cursorCreatedAt, cursorID}
	if issueID != "" {
		args = append(args, issueID)
	}

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("events since cursor (%v, %q) issue %q: %w", cursorCreatedAt, cursorID, issueID, err)
	}
	defer rows.Close()

	return scanEvents(rows)
}

// EventsSinceQuery returns the exact SQL EventsSinceInTx executes for the given
// issueID scope and already-clamped limit, with ? placeholders bound in order by
// the caller: created_at (sargable lower bound), created_at (strict), id
// (same-second tie-break), and issue_id when issueID != "". It is exported so
// the backend sargability guard EXPLAINs this production string rather than a
// hand-copied literal — a change to the SARGABLE predicate here then breaks the
// guard.
//
//nolint:gosec // G201: limit is an int the caller clamps; every runtime value is a bound parameter.
func EventsSinceQuery(issueID string, limit int) string {
	query := `
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM events
		WHERE created_at >= ? AND ((created_at > ?) OR (id > ?))`
	if issueID != "" {
		query += " AND issue_id = ?"
	}
	query += fmt.Sprintf(" ORDER BY created_at ASC, id ASC LIMIT %d", limit)
	return query
}

func scanEvents(rows *sql.Rows) ([]*types.Event, error) {
	var events []*types.Event
	for rows.Next() {
		var event types.Event
		var oldValue, newValue, comment sql.NullString
		if err := rows.Scan(&event.ID, &event.IssueID, &event.EventType, &event.Actor,
			&oldValue, &newValue, &comment, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if oldValue.Valid {
			event.OldValue = &oldValue.String
		}
		if newValue.Valid {
			event.NewValue = &newValue.String
		}
		if comment.Valid {
			event.Comment = &comment.String
		}
		events = append(events, &event)
	}
	return events, rows.Err()
}
