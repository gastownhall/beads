package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

const limitClause = " LIMIT ?"

// AddComment adds a comment to an issue
func (s *SQLiteStorage) AddComment(ctx context.Context, issueID, actor, comment string) error {
	return s.withTx(ctx, func(conn *sql.Conn) error {
		// Update issue updated_at timestamp first to verify issue exists
		now := time.Now()
		res, err := conn.ExecContext(ctx, `
			UPDATE issues SET updated_at = ? WHERE id = ?
		`, now, issueID)
		if err != nil {
			return fmt.Errorf("failed to update timestamp: %w", err)
		}

		rows, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		}
		if rows == 0 {
			return fmt.Errorf("issue %s not found", issueID)
		}

		_, err = conn.ExecContext(ctx, `
			INSERT INTO events (issue_id, event_type, actor, comment)
			VALUES (?, ?, ?, ?)
		`, issueID, types.EventCommented, actor, comment)
		if err != nil {
			return fmt.Errorf("failed to add comment: %w", err)
		}

		return nil
	})
}

// GetEvents returns the event history for an issue
func (s *SQLiteStorage) GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error) {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	args := []interface{}{issueID}
	limitSQL := ""
	if limit > 0 {
		limitSQL = limitClause
		args = append(args, limit)
	}

	// #nosec G201 - safe SQL with controlled formatting
	query := fmt.Sprintf(`
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM events
		WHERE issue_id = ?
		ORDER BY created_at DESC
		%s
	`, limitSQL)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []*types.Event
	for rows.Next() {
		var event types.Event
		var oldValue, newValue, comment sql.NullString

		err := rows.Scan(
			&event.ID, &event.IssueID, &event.EventType, &event.Actor,
			&oldValue, &newValue, &comment, &event.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
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

	return events, nil
}

// GetAllEventsSince returns all events with ID greater than sinceID, ordered by ID ascending.
func (s *SQLiteStorage) GetAllEventsSince(ctx context.Context, sinceID int64) ([]*types.Event, error) {
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, issue_id, event_type, actor, old_value, new_value, comment, created_at
		FROM events
		WHERE id > ?
		ORDER BY id ASC
	`, sinceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get events since %d: %w", sinceID, err)
	}
	defer func() { _ = rows.Close() }()

	var events []*types.Event
	for rows.Next() {
		var event types.Event
		var oldValue, newValue, comment sql.NullString

		err := rows.Scan(
			&event.ID, &event.IssueID, &event.EventType, &event.Actor,
			&oldValue, &newValue, &comment, &event.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
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

	if err := rows.Err(); err != nil {
		return nil, wrapDBError("iterate events", err)
	}
	return events, nil
}

// GetStatistics returns aggregate statistics
func (s *SQLiteStorage) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	var stats types.Statistics

	// Get counts
	// (bd-6v2: also count pinned issues)
	// (bd-4jr: also count deferred issues)
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN status = 'open' THEN 1 ELSE 0 END), 0) as open,
			COALESCE(SUM(CASE WHEN status = 'in_progress' THEN 1 ELSE 0 END), 0) as in_progress,
			COALESCE(SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END), 0) as closed,
			COALESCE(SUM(CASE WHEN status = 'deferred' THEN 1 ELSE 0 END), 0) as deferred,
			COALESCE(SUM(CASE WHEN pinned = 1 THEN 1 ELSE 0 END), 0) as pinned
		FROM issues
	`).Scan(&stats.TotalIssues, &stats.OpenIssues, &stats.InProgressIssues, &stats.ClosedIssues, &stats.DeferredIssues, &stats.PinnedIssues)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue counts: %w", err)
	}

	// Get blocked count
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT i.id)
		FROM issues i
		JOIN dependencies d ON i.id = d.issue_id
		JOIN issues blocker ON d.depends_on_id = blocker.id
		WHERE i.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		  AND d.type = 'blocks'
		  AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
	`).Scan(&stats.BlockedIssues)
	if err != nil {
		return nil, fmt.Errorf("failed to get blocked count: %w", err)
	}

	// Get ready count
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM issues i
		WHERE i.status = 'open'
		  AND NOT EXISTS (
		    SELECT 1 FROM dependencies d
		    JOIN issues blocker ON d.depends_on_id = blocker.id
		    WHERE d.issue_id = i.id
		      AND d.type = 'blocks'
		      AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		  )
	`).Scan(&stats.ReadyIssues)
	if err != nil {
		return nil, fmt.Errorf("failed to get ready count: %w", err)
	}

	// Get average lead time (hours from created to closed)
	var avgLeadTime sql.NullFloat64
	err = s.db.QueryRowContext(ctx, `
		SELECT AVG(
			(julianday(closed_at) - julianday(created_at)) * 24
		)
		FROM issues
		WHERE closed_at IS NOT NULL
	`).Scan(&avgLeadTime)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get lead time: %w", err)
	}
	if avgLeadTime.Valid {
		stats.AverageLeadTime = avgLeadTime.Float64
	}

	// Get epics eligible for closure count
	err = s.db.QueryRowContext(ctx, `
		WITH epic_children AS (
			SELECT 
				d.depends_on_id AS epic_id,
				i.status AS child_status
			FROM dependencies d
			JOIN issues i ON i.id = d.issue_id
			WHERE d.type = 'parent-child'
		),
		epic_stats AS (
			SELECT 
				epic_id,
				COUNT(*) AS total_children,
				SUM(CASE WHEN child_status = 'closed' THEN 1 ELSE 0 END) AS closed_children
			FROM epic_children
			GROUP BY epic_id
		)
		SELECT COUNT(*)
		FROM issues i
		JOIN epic_stats es ON es.epic_id = i.id
		WHERE i.issue_type = 'epic'
		  AND i.status != 'closed'
		  AND es.total_children > 0
		  AND es.closed_children = es.total_children
	`).Scan(&stats.EpicsEligibleForClosure)
	if err != nil {
		return nil, fmt.Errorf("failed to get eligible epics count: %w", err)
	}

	return &stats, nil
}

// GetMoleculeProgress returns efficient progress stats for a molecule.
// Uses indexed queries on dependencies table instead of loading all steps.
func (s *SQLiteStorage) GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error) {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	// First get the molecule's title
	var title string
	err := s.db.QueryRowContext(ctx, `SELECT title FROM issues WHERE id = ?`, moleculeID).Scan(&title)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("molecule not found: %s", moleculeID)
		}
		return nil, fmt.Errorf("failed to get molecule: %w", err)
	}

	stats := &types.MoleculeProgressStats{
		MoleculeID:    moleculeID,
		MoleculeTitle: title,
	}

	// Get counts from direct children via parent-child dependency
	// Uses idx_dependencies_depends_on_type index
	err = s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN i.status = 'closed' THEN 1 ELSE 0 END), 0) as completed,
			COALESCE(SUM(CASE WHEN i.status = 'in_progress' THEN 1 ELSE 0 END), 0) as in_progress
		FROM dependencies d
		JOIN issues i ON d.issue_id = i.id
		WHERE d.depends_on_id = ? AND d.type = 'parent-child'
	`, moleculeID).Scan(&stats.Total, &stats.Completed, &stats.InProgress)
	if err != nil {
		return nil, fmt.Errorf("failed to get child counts: %w", err)
	}

	// Get first in_progress step ID (for "current step" display)
	var currentStepID sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT i.id
		FROM dependencies d
		JOIN issues i ON d.issue_id = i.id
		WHERE d.depends_on_id = ? AND d.type = 'parent-child' AND i.status = 'in_progress'
		ORDER BY i.created_at
		LIMIT 1
	`, moleculeID).Scan(&currentStepID)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get current step: %w", err)
	}
	if currentStepID.Valid {
		stats.CurrentStepID = currentStepID.String
	}

	// Get first and last closure times for rate calculation
	var firstClosed, lastClosed sql.NullTime
	err = s.db.QueryRowContext(ctx, `
		SELECT MIN(i.closed_at), MAX(i.closed_at)
		FROM dependencies d
		JOIN issues i ON d.issue_id = i.id
		WHERE d.depends_on_id = ? AND d.type = 'parent-child' AND i.status = 'closed'
	`, moleculeID).Scan(&firstClosed, &lastClosed)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get closure times: %w", err)
	}
	if firstClosed.Valid {
		stats.FirstClosed = &firstClosed.Time
	}
	if lastClosed.Valid {
		stats.LastClosed = &lastClosed.Time
	}

	return stats, nil
}
