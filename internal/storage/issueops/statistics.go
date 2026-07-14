package issueops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// ScanIssueCountsInTx populates the count fields (TotalIssues, OpenIssues,
// InProgressIssues, ClosedIssues, DeferredIssues, PinnedIssues) of stats from
// the issues table. It does NOT compute BlockedIssues or ReadyIssues — callers
// fill those in using their own blocked-ID computation strategy.
func ScanIssueCountsInTx(ctx context.Context, tx DBTX, stats *types.Statistics) error {
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

// GetStatisticsInTx computes the full summary statistics (counts + blocked + ready)
// in one transaction, using only the normal issues table — no version-control state —
// so it is portable across every SQL backend. Behaviorally identical to the Dolt and
// embedded-Dolt implementations: ScanIssueCountsInTx for the status counts, a direct
// blocked count (the is_blocked flag is maintained in-tx by the shared layer), then
// ReadyIssues = OpenIssues - BlockedIssues clamped at zero.
func GetStatisticsInTx(ctx context.Context, tx DBTX) (*types.Statistics, error) {
	stats := &types.Statistics{}
	if err := ScanIssueCountsInTx(ctx, tx, stats); err != nil {
		return nil, err
	}
	var blocked int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM issues
		WHERE is_blocked = 1 AND status <> 'closed' AND status <> 'pinned'
	`).Scan(&blocked); err != nil {
		return nil, fmt.Errorf("count blocked issues: %w", err)
	}
	stats.BlockedIssues = blocked
	stats.ReadyIssues = stats.OpenIssues - stats.BlockedIssues
	if stats.ReadyIssues < 0 {
		stats.ReadyIssues = 0
	}
	return stats, nil
}
