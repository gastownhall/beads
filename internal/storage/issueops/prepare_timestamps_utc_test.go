package issueops

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestPrepareIssueForInsertNormalizesOptionalTimestampsToUTC is a born-failing
// reproduction for #5765: on JSONL import an optional timestamp carrying a
// non-UTC offset (e.g. closed_at with -04:00 from an older SQLite-era export)
// kept its local wall-clock digits, so the stored instant shifted by the offset
// while created_at/updated_at were converted correctly. PrepareIssueForInsert
// must normalize every set optional timestamp to true UTC, exactly as it already
// does for CreatedAt/UpdatedAt.
func TestPrepareIssueForInsertNormalizesOptionalTimestampsToUTC(t *testing.T) {
	// America/New_York style EDT offset, without depending on tz database load.
	edt := time.FixedZone("EDT", -4*60*60)

	// 2026-08-13T10:20:22-04:00 == 2026-08-13T14:20:22Z (the reporter's imp-100).
	closed := time.Date(2026, 8, 13, 10, 20, 22, 0, edt)
	started := time.Date(2026, 8, 13, 10, 17, 47, 0, edt)
	due := time.Date(2026, 8, 20, 9, 0, 0, 0, edt)
	deferUntil := time.Date(2026, 8, 15, 8, 30, 0, 0, edt)

	issue := &types.Issue{
		ID:         "imp-100",
		Title:      "EDT offset",
		IssueType:  types.TypeTask,
		Status:     types.StatusClosed,
		Priority:   2,
		CreatedAt:  started,
		UpdatedAt:  closed,
		ClosedAt:   &closed,
		StartedAt:  &started,
		DueAt:      &due,
		DeferUntil: &deferUntil,
	}

	if err := PrepareIssueForInsert(issue, nil, nil); err != nil {
		t.Fatalf("PrepareIssueForInsert: %v", err)
	}

	checks := []struct {
		name string
		got  *time.Time
		want time.Time
	}{
		{"closed_at", issue.ClosedAt, closed},
		{"started_at", issue.StartedAt, started},
		{"due_at", issue.DueAt, due},
		{"defer_until", issue.DeferUntil, deferUntil},
	}
	for _, c := range checks {
		if c.got == nil {
			t.Fatalf("%s was cleared to nil", c.name)
		}
		if c.got.Location() != time.UTC {
			t.Errorf("%s location = %v, want UTC (offset digits kept instead of converting)", c.name, c.got.Location())
		}
		if !c.got.Equal(c.want) {
			t.Errorf("%s instant = %s, want %s", c.name, c.got.Format(time.RFC3339), c.want.UTC().Format(time.RFC3339))
		}
	}

	// The reported field, checked on its exact wall-clock digits: -04:00 dropped
	// means 10:20:22Z (wrong); converted means 14:20:22Z (correct).
	if wall := issue.ClosedAt.Format("2006-01-02T15:04:05Z07:00"); wall != "2026-08-13T14:20:22Z" {
		t.Errorf("closed_at = %s, want 2026-08-13T14:20:22Z", wall)
	}
}
