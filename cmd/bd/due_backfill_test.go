package main

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func backfillIssue(id string, created time.Time, mutate func(*types.Issue)) *types.Issue {
	issue := &types.Issue{
		ID:        id,
		Title:     "issue " + id,
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		Priority:  2,
		CreatedAt: created,
	}
	if mutate != nil {
		mutate(issue)
	}
	return issue
}

func day(d int) time.Time { return time.Date(2026, 3, d, 9, 0, 0, 0, time.UTC) }

// The plan is the whole contract of the dry run: what it counts is what --apply
// writes. This pins which beads are in scope and which are deliberately not.
func TestPlanDueBackfillSelectsOnlyUndatedWork(t *testing.T) {
	closedAt := day(2)
	issues := []*types.Issue{
		backfillIssue("bd-1", day(1), nil), // in scope
		backfillIssue("bd-2", day(3), nil), // in scope
		backfillIssue("bd-already", day(1), func(i *types.Issue) { // skipped: has a date
			due := day(20)
			i.DueAt = &due
		}),
		backfillIssue("bd-closed", day(1), func(i *types.Issue) { // skipped: no work left
			i.Status = types.StatusClosed
			i.ClosedAt = &closedAt
		}),
		backfillIssue("bd-event", day(1), func(i *types.Issue) { i.IssueType = types.TypeEvent }),
		backfillIssue("bd-wisp", day(1), func(i *types.Issue) { i.Ephemeral = true }),
		backfillIssue("bd-template", day(1), func(i *types.Issue) { i.IsTemplate = true }),
		backfillIssue("bd-federated", day(1), func(i *types.Issue) { i.SourceSystem = "github" }),
		nil, // a nil row must not panic the report
	}

	report := planDueBackfill(issues, 7*24*time.Hour, day(1))

	if report.Candidates != 2 {
		t.Fatalf("Candidates = %d, want 2 (rows: %+v)", report.Candidates, report.Rows)
	}
	if report.AlreadyDated != 1 {
		t.Errorf("AlreadyDated = %d, want 1", report.AlreadyDated)
	}
	// Every exclusion is counted and named, never inferred from the gap
	// between scanned and candidates.
	if report.SkippedClosed != 1 {
		t.Errorf("SkippedClosed = %d, want 1", report.SkippedClosed)
	}
	wantExempt := map[string]int{"event": 1, "wisp": 1, "template": 1, "federated": 1}
	for class, n := range wantExempt {
		if report.SkippedExempt[class] != n {
			t.Errorf("SkippedExempt[%s] = %d, want %d (all: %v)", class, report.SkippedExempt[class], n, report.SkippedExempt)
		}
	}
	if len(report.SkippedExempt) != len(wantExempt) {
		t.Errorf("SkippedExempt = %v, want exactly %v", report.SkippedExempt, wantExempt)
	}
	if report.Scanned != len(issues) {
		t.Errorf("Scanned = %d, want %d", report.Scanned, len(issues))
	}
	if report.Applied {
		t.Error("planDueBackfill marked the report applied; the plan writes nothing")
	}
	if report.Updated != 0 {
		t.Errorf("Updated = %d, want 0 for a plan", report.Updated)
	}
	gotIDs := []string{report.Rows[0].ID, report.Rows[1].ID}
	if gotIDs[0] != "bd-1" || gotIDs[1] != "bd-2" {
		t.Errorf("rows = %v, want oldest-first [bd-1 bd-2]", gotIDs)
	}
}

// Each bead is dated from its OWN created_at, so the relative order of a
// backlog survives the backfill instead of collapsing onto one date.
func TestPlanDueBackfillDatesFromEachCreatedAt(t *testing.T) {
	interval := 7 * 24 * time.Hour
	issues := []*types.Issue{
		backfillIssue("bd-old", day(1), nil),
		backfillIssue("bd-new", day(10), nil),
	}
	report := planDueBackfill(issues, interval, day(1))
	for _, row := range report.Rows {
		if want := row.CreatedAt.Add(interval); !row.ProposedDue.Equal(want) {
			t.Errorf("%s proposed due = %v, want %v", row.ID, row.ProposedDue, want)
		}
	}
	if !report.Rows[0].ProposedDue.Before(report.Rows[1].ProposedDue) {
		t.Error("backfill collapsed the backlog's ordering")
	}
	if report.OldestDue == nil || !report.OldestDue.Equal(day(8)) {
		t.Errorf("OldestDue = %v, want %v", report.OldestDue, day(8))
	}
	if report.NewestDue == nil || !report.NewestDue.Equal(day(17)) {
		t.Errorf("NewestDue = %v, want %v", report.NewestDue, day(17))
	}
}

func TestPlanDueBackfillCountsByTypeAndStatus(t *testing.T) {
	issues := []*types.Issue{
		backfillIssue("bd-1", day(1), nil),
		backfillIssue("bd-2", day(2), func(i *types.Issue) { i.IssueType = types.TypeBug }),
		backfillIssue("bd-3", day(3), func(i *types.Issue) { i.Status = types.StatusInProgress }),
	}
	report := planDueBackfill(issues, 24*time.Hour, day(1))
	if report.ByType["task"] != 2 || report.ByType["bug"] != 1 {
		t.Errorf("ByType = %v, want task=2 bug=1", report.ByType)
	}
	if report.ByStatus["open"] != 2 || report.ByStatus["in_progress"] != 1 {
		t.Errorf("ByStatus = %v, want open=2 in_progress=1", report.ByStatus)
	}
}

func TestPlanDueBackfillEmptyRepository(t *testing.T) {
	report := planDueBackfill(nil, 24*time.Hour, day(1))
	if report.Candidates != 0 || len(report.Rows) != 0 || report.OldestDue != nil {
		t.Errorf("empty plan = %+v, want no candidates", report)
	}
}

// A bead older than the interval must not be dated in the past: that would
// fire every one of them on the next ready read. Its date is floored to
// now + interval, and the report says so.
func TestPlanDueBackfillFloorsDatesToNowPlusInterval(t *testing.T) {
	interval := 7 * 24 * time.Hour
	now := day(20)
	issues := []*types.Issue{
		backfillIssue("bd-ancient", day(1), nil), // created+7d = day 8, in the past
		backfillIssue("bd-edge", day(13), nil),   // created+7d = day 20 = now, still floored
		backfillIssue("bd-recent", day(21), nil), // created+7d = day 28, past the floor, kept
	}
	report := planDueBackfill(issues, interval, now)
	if report.Floored != 2 {
		t.Fatalf("Floored = %d, want 2 (rows: %+v)", report.Floored, report.Rows)
	}
	floor := now.Add(interval)
	for _, row := range report.Rows {
		switch row.ID {
		case "bd-ancient", "bd-edge":
			// Floored rows land INSIDE the window that starts at the floor,
			// not exactly on it: a whole legacy backlog stacked on one instant
			// fires as one herd (see spreadFlooredDue).
			if !row.Floored || row.ProposedDue.Before(floor) || !row.ProposedDue.Before(floor.Add(interval)) {
				t.Errorf("%s: proposed %v floored=%v, want floored=true inside [%v, %v)",
					row.ID, row.ProposedDue, row.Floored, floor, floor.Add(interval))
			}
		case "bd-recent":
			if row.Floored || !row.ProposedDue.Equal(day(28)) {
				t.Errorf("%s: proposed %v floored=%v, want %v floored=false", row.ID, row.ProposedDue, row.Floored, day(28))
			}
		}
		if row.ProposedDue.Before(now) {
			t.Errorf("%s: proposed due %v is in the past (now %v)", row.ID, row.ProposedDue, now)
		}
	}
	if report.OldestDue == nil || report.OldestDue.Before(now) {
		t.Errorf("OldestDue = %v, want no proposed date before now", report.OldestDue)
	}
}

func TestParseBackfillInterval(t *testing.T) {
	tests := []struct {
		raw  string
		want time.Duration
	}{
		{"+7d", 7 * 24 * time.Hour},
		{"+2w", 14 * 24 * time.Hour},
		{"+12h", 12 * time.Hour},
		{"3d", 3 * 24 * time.Hour},
	}
	for _, tc := range tests {
		got, err := parseBackfillInterval(tc.raw)
		if err != nil {
			t.Errorf("parseBackfillInterval(%q) error = %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseBackfillInterval(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	// A cron expression is a schedule, not an offset: it cannot say "how far
	// past created_at", so it is refused rather than silently reinterpreted.
	for _, bad := range []string{"0 9 * * 1", "-7d", "next tuesday", "0d"} {
		if _, err := parseBackfillInterval(bad); err == nil {
			t.Errorf("parseBackfillInterval(%q) error = nil, want a refusal", bad)
		}
	}
}

func TestFormatCountMapIsStable(t *testing.T) {
	counts := map[string]int{"task": 3, "bug": 1, "chore": 2}
	want := "bug=1, chore=2, task=3"
	for i := 0; i < 5; i++ {
		if got := formatCountMap(counts); got != want {
			t.Fatalf("formatCountMap = %q, want %q", got, want)
		}
	}
	if got := formatCountMap(nil); got != "(none)" {
		t.Errorf("formatCountMap(nil) = %q, want (none)", got)
	}
}

// The backfill's report is a promise: a human reads the proposed dates, then
// re-runs with --apply expecting those dates. A spread drawn at random would
// break that — the dry run would describe a different backfill than the one
// that runs — so the offset must be derived from the bead's identity.
func TestSpreadFlooredDueIsDeterministicAndInsideTheWindow(t *testing.T) {
	interval := 7 * 24 * time.Hour
	floor := day(20)

	first := spreadFlooredDue("bd-ancient", floor, interval)
	if second := spreadFlooredDue("bd-ancient", floor, interval); !first.Equal(second) {
		t.Errorf("same id produced %v then %v; the report and --apply would disagree", first, second)
	}
	if first.Before(floor) || !first.Before(floor.Add(interval)) {
		t.Errorf("proposed %v is outside [%v, %v)", first, floor, floor.Add(interval))
	}

	// The point of the spread is that a backlog does not collapse onto one
	// instant, so distinct beads must genuinely land on distinct dates.
	seen := map[time.Time]string{}
	for _, id := range []string{"bd-1", "bd-2", "bd-3", "bd-4", "bd-5", "bd-6", "bd-7", "bd-8"} {
		at := spreadFlooredDue(id, floor, interval)
		if other, clash := seen[at]; clash {
			t.Errorf("%s and %s both land on %v", id, other, at)
		}
		seen[at] = id
	}

	// A degenerate interval has no window to spread across and must not panic
	// or produce a date before the floor.
	if got := spreadFlooredDue("bd-1", floor, 0); !got.Equal(floor) {
		t.Errorf("zero interval produced %v, want the floor %v", got, floor)
	}
}
