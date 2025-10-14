package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// AddComment adds a comment to an issue
func (s *SQLiteStorage) AddComment(ctx context.Context, issueID, actor, comment string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, issueID, types.EventCommented, actor, comment)
	if err != nil {
		return fmt.Errorf("failed to add comment: %w", err)
	}

	// Update issue updated_at timestamp
	now := time.Now()
	_, err = tx.ExecContext(ctx, `
		UPDATE issues SET updated_at = ? WHERE id = ?
	`, now, issueID)
	if err != nil {
		return fmt.Errorf("failed to update timestamp: %w", err)
	}

	// Mark issue as dirty for incremental export
	_, err = tx.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, issueID, now)
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}

	return tx.Commit()
}

// GetEvents returns the event history for an issue
func (s *SQLiteStorage) GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error) {
	args := []interface{}{issueID}
	limitSQL := ""
	if limit > 0 {
		limitSQL = " LIMIT ?"
		args = append(args, limit)
	}

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
	defer rows.Close()

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

// GetStatistics returns aggregate statistics
func (s *SQLiteStorage) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	var stats types.Statistics

	// Get counts
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) as total,
			SUM(CASE WHEN status = 'open' THEN 1 ELSE 0 END) as open,
			SUM(CASE WHEN status = 'in_progress' THEN 1 ELSE 0 END) as in_progress,
			SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END) as closed
		FROM issues
	`).Scan(&stats.TotalIssues, &stats.OpenIssues, &stats.InProgressIssues, &stats.ClosedIssues)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue counts: %w", err)
	}

	// Get blocked count
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT i.id)
		FROM issues i
		JOIN dependencies d ON i.id = d.issue_id
		JOIN issues blocker ON d.depends_on_id = blocker.id
		WHERE i.status IN ('open', 'in_progress', 'blocked')
		  AND d.type = 'blocks'
		  AND blocker.status IN ('open', 'in_progress', 'blocked')
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
		    JOIN issues blocked ON d.depends_on_id = blocked.id
		    WHERE d.issue_id = i.id
		      AND d.type = 'blocks'
		      AND blocked.status IN ('open', 'in_progress', 'blocked')
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

	return &stats, nil
}
