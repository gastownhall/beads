//go:build cgo

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
		if err := issueops.ScanIssueCountsInTx(ctx, tx, stats); err != nil {
			return err
		}

		var blockedCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM issues
			WHERE is_blocked = 1 AND status <> 'closed' AND status <> 'pinned'
		`).Scan(&blockedCount); err != nil {
			return err
		}
		stats.BlockedIssues = &blockedCount
		ready := stats.OpenIssues - blockedCount
		if ready < 0 {
			ready = 0
		}
		stats.ReadyIssues = &ready
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("embeddeddolt: get statistics: %w", err)
	}
	return stats, nil
}

// GetStatisticsNoBlocked returns aggregate counts without the blocked-set traversal.
// BlockedIssues and ReadyIssues are nil in the result (readiness needs the blocked
// set). Use for bd stats --no-blocked fast path.
func (s *EmbeddedDoltStore) GetStatisticsNoBlocked(ctx context.Context) (*types.Statistics, error) {
	stats := &types.Statistics{}
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		return issueops.ScanIssueCountsInTx(ctx, tx, stats)
	})
	if err != nil {
		return nil, fmt.Errorf("embeddeddolt: get statistics: %w", err)
	}
	// BlockedIssues stays nil; ReadyIssues not computable without blocked set.
	return stats, nil
}
