package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// ScanIssueCountsInTx populates the count fields (TotalIssues, OpenIssues,
// InProgressIssues, ClosedIssues, DeferredIssues, PinnedIssues) of stats from
// the issues table. It does NOT compute BlockedIssues or ReadyIssues — callers
// fill those in using their own blocked-ID computation strategy.
func ScanIssueCountsInTx(ctx context.Context, tx *sql.Tx, stats *types.Statistics) error {
	if err := tx.QueryRowContext(ctx, `
		SELECT
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN status = 'open' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'in_progress' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'closed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'deferred' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN pinned = 1 THEN 1 ELSE 0 END), 0)
		FROM issues
	`).Scan(
		&stats.TotalIssues,
		&stats.OpenIssues,
		&stats.InProgressIssues,
		&stats.ClosedIssues,
		&stats.DeferredIssues,
		&stats.PinnedIssues,
	); err != nil {
		return fmt.Errorf("scan issue counts: %w", err)
	}
	return nil
}
