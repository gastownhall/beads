//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func (s *EmbeddedDoltStore) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	stats := &types.Statistics{}
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
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
			return err
		}

		blockedIDs, _, err := issueops.ComputeBlockedIDsInTx(ctx, tx, true)
		if err != nil {
			return err
		}
		stats.BlockedIssues = len(blockedIDs)
		stats.ReadyIssues = stats.OpenIssues - stats.BlockedIssues
		if stats.ReadyIssues < 0 {
			stats.ReadyIssues = 0
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("embeddeddolt: get statistics: %w", err)
	}
	return stats, nil
}
