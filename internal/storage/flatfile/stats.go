package flatfile

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// GetStatistics returns aggregate metrics across all issues. Mirrors
// issueops.GetStatisticsInTx: six status counts, BlockedIssues from the
// is_blocked projection (active issues with an active blocker), and
// ReadyIssues = OpenIssues - BlockedIssues clamped at zero.
func (s *FlatFileStore) GetStatistics(_ context.Context) (*types.Statistics, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}

	stats := &types.Statistics{}
	blocked := computeBlockedSet(all)

	for _, issue := range all {
		if issue.Ephemeral {
			// Wisps live in the wisps table on SQL backends;
			// issueops.ScanIssueCountsInTx and the blocked COUNT(*) both scan
			// FROM issues only, so ephemeral issues never enter the counts.
			continue
		}
		stats.TotalIssues++

		switch issue.Status {
		case types.StatusOpen:
			stats.OpenIssues++
		case types.StatusInProgress:
			stats.InProgressIssues++
		case types.StatusClosed:
			stats.ClosedIssues++
		case types.StatusDeferred:
			stats.DeferredIssues++
		case types.StatusBlocked, types.StatusPinned, types.StatusHooked:
			// No dedicated status counter, matching the SQL reference: the
			// blocked count comes from the is_blocked projection below and
			// PinnedIssues from the pinned column flag.
		}

		if issue.Pinned {
			stats.PinnedIssues++
		}

		// BlockedIssues mirrors `SELECT COUNT(*) FROM issues WHERE is_blocked
		// = 1 AND status NOT IN (closed, pinned)`; computeBlockedSet only ever
		// marks active (non-closed, non-pinned) issues, and the ephemeral skip
		// above keeps blocked wisps out, matching the issues-only scan.
		if blocked[issue.ID] {
			stats.BlockedIssues++
		}
	}

	stats.ReadyIssues = stats.OpenIssues - stats.BlockedIssues
	if stats.ReadyIssues < 0 {
		stats.ReadyIssues = 0
	}

	// AverageLeadTime and EpicsEligibleForClosure stay zero: no SQL backend
	// fills them (issueops.GetStatisticsInTx computes only counts + blocked +
	// ready), and bd status gates those lines on > 0, so filling them here
	// would diverge cross-backend output for identical data.

	return stats, nil
}
