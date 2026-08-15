package db

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestNormalizeIssueTimestampsConvertsOptionalTimestampsToUTC covers the
// proxied-server insert path's share of #5765: normalizeIssueTimestamps must
// convert an offset-bearing closed_at (and its sibling optional timestamps) to
// UTC, matching the embedded PrepareIssueForInsert path, so the stored instant
// is not shifted by the host's UTC offset on JSONL import.
func TestNormalizeIssueTimestampsConvertsOptionalTimestampsToUTC(t *testing.T) {
	est := time.FixedZone("EST", -5*60*60)
	// 2026-03-07T22:06:41-05:00 == 2026-03-08T03:06:41Z (the reporter's imp-200).
	closed := time.Date(2026, 3, 7, 22, 6, 41, 0, est)
	compacted := time.Date(2026, 3, 8, 1, 0, 0, 0, est)

	issue := &types.Issue{
		ID:          "imp-200",
		Title:       "EST offset",
		IssueType:   types.TypeTask,
		Status:      types.StatusClosed,
		CreatedAt:   closed,
		UpdatedAt:   closed,
		ClosedAt:    &closed,
		CompactedAt: &compacted,
	}

	normalizeIssueTimestamps(issue)

	if issue.ClosedAt.Location() != time.UTC {
		t.Errorf("closed_at location = %v, want UTC", issue.ClosedAt.Location())
	}
	if wall := issue.ClosedAt.Format("2006-01-02T15:04:05Z07:00"); wall != "2026-03-08T03:06:41Z" {
		t.Errorf("closed_at = %s, want 2026-03-08T03:06:41Z", wall)
	}
	if issue.CompactedAt.Location() != time.UTC {
		t.Errorf("compacted_at location = %v, want UTC", issue.CompactedAt.Location())
	}
}
