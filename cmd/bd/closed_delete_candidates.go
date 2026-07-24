package main

import (
	"time"

	"github.com/steveyegge/beads/internal/types"
)

type closedDeletionCandidateStats struct {
	PinnedSkipped          int
	NilSkipped             int
	NonClosedSkipped       int
	MissingClosedAtSkipped int
	RecentClosedAtSkipped  int
}

func (s closedDeletionCandidateStats) SafetySkipped() int {
	return s.NilSkipped + s.NonClosedSkipped + s.MissingClosedAtSkipped + s.RecentClosedAtSkipped
}

func filterClosedDeletionCandidates(issues []*types.Issue, cutoff *time.Time) ([]*types.Issue, closedDeletionCandidateStats) {
	filtered := make([]*types.Issue, 0, len(issues))
	var stats closedDeletionCandidateStats

	for _, issue := range issues {
		if issue == nil {
			stats.NilSkipped++
			continue
		}
		if issue.Pinned {
			stats.PinnedSkipped++
			continue
		}
		if issue.Status != types.StatusClosed {
			stats.NonClosedSkipped++
			continue
		}
		if issue.ClosedAt == nil {
			stats.MissingClosedAtSkipped++
			continue
		}
		if cutoff != nil && !issue.ClosedAt.Before(*cutoff) {
			stats.RecentClosedAtSkipped++
			continue
		}
		filtered = append(filtered, issue)
	}

	return filtered, stats
}

func warnClosedDeletionSafetySkips(stats closedDeletionCandidateStats) {
	if stats.SafetySkipped() == 0 {
		return
	}
	WarnError("skipped %d deletion candidate(s) after closed_at safety recheck (nil=%d, non_closed=%d, missing_closed_at=%d, too_recent=%d)",
		stats.SafetySkipped(),
		stats.NilSkipped,
		stats.NonClosedSkipped,
		stats.MissingClosedAtSkipped,
		stats.RecentClosedAtSkipped,
	)
}
