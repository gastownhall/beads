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
