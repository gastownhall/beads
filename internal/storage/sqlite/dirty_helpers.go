package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// markDirty marks a single issue as dirty for incremental export
func markDirty(ctx context.Context, conn *sql.Conn, issueID string) error {
	_, err := conn.ExecContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`, issueID, time.Now())
	if err != nil {
		return fmt.Errorf("failed to mark issue dirty: %w", err)
	}
	return nil
}

// markDirtyBatch marks multiple issues as dirty for incremental export
func markDirtyBatch(ctx context.Context, conn *sql.Conn, issues []*types.Issue) error {
	stmt, err := conn.PrepareContext(ctx, `
		INSERT INTO dirty_issues (issue_id, marked_at)
		VALUES (?, ?)
		ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare dirty statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	dirtyTime := time.Now()
	for _, issue := range issues {
		_, err = stmt.ExecContext(ctx, issue.ID, dirtyTime)
		if err != nil {
			return fmt.Errorf("failed to mark issue %s dirty: %w", issue.ID, err)
		}
	}
	return nil
}
