package db

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestNormalizeIssueTimestampsConvertsOptionalTimestampsToUTC is a unit-level pin
// on this route's half of the #5765 wiring. This route is already offset-safe on
// its own: domain/db runs on go-sql-driver, whose DSN Loc defaults to time.UTC and
// which converts bound time.Time values into that location on both the interpolate
// and prepared/binary paths, and no production DSN sets loc=. The embedded route is
// the one that actually discards offsets, formatting in the value's own zone via
// vitess BuildBindVariable. normalizeIssueTimestamps is therefore defense-in-depth
// for parity with the embedded PrepareIssueForInsert path, and this test pins that
// wiring rather than a live shift on this route.
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
