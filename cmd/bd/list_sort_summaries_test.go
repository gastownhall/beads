package main

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// sortSummariesFixture builds a stable fixture covering all sortable fields.
// Each summary's ID encodes the expected creation/update ordering so tests can
// assert ordering independently of the Go map-iteration nondeterminism used
// elsewhere in the suite.
func sortSummariesFixture() []*types.IssueSummary {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	t2 := t0.Add(2 * time.Hour)
	closed1 := t0.Add(30 * time.Minute)
	closed2 := t0.Add(90 * time.Minute)

	return []*types.IssueSummary{
		{
			ID:        "bd-b",
			Title:     "Mango",
			Status:    types.StatusInProgress,
			Priority:  2,
			IssueType: types.TypeTask,
			Assignee:  "bob",
			CreatedAt: t1,
			UpdatedAt: t2,
			ClosedAt:  nil,
		},
		{
			ID:        "bd-a",
			Title:     "apple",
			Status:    types.StatusOpen,
			Priority:  0,
			IssueType: types.TypeBug,
			Assignee:  "alice",
			CreatedAt: t2,
			UpdatedAt: t0,
			ClosedAt:  &closed2,
		},
		{
			ID:        "bd-c",
			Title:     "BANANA",
			Status:    types.StatusClosed,
			Priority:  1,
			IssueType: types.TypeFeature,
			Assignee:  "carol",
			CreatedAt: t0,
			UpdatedAt: t1,
			ClosedAt:  &closed1,
		},
	}
}

func summaryIDs(summaries []*types.IssueSummary) []string {
	out := make([]string, len(summaries))
	for i, s := range summaries {
		out[i] = s.ID
	}
	return out
}

func assertIDOrder(t *testing.T, summaries []*types.IssueSummary, want ...string) {
	t.Helper()
	got := summaryIDs(summaries)
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order mismatch: got %v want %v", got, want)
		}
	}
}

func TestSortSummaries_EmptySortByIsNoOp(t *testing.T) {
	summaries := sortSummariesFixture()
	sortSummaries(summaries, "", false)
	assertIDOrder(t, summaries, "bd-b", "bd-a", "bd-c")
}

func TestSortSummaries_Priority(t *testing.T) {
	summaries := sortSummariesFixture()
	sortSummaries(summaries, "priority", false)
	// Lower priority numbers come first (P0 > P1 > P2).
	assertIDOrder(t, summaries, "bd-a", "bd-c", "bd-b")
}

func TestSortSummaries_Created_NewestFirst(t *testing.T) {
	summaries := sortSummariesFixture()
	sortSummaries(summaries, "created", false)
	// Default order is descending: newest CreatedAt first.
	assertIDOrder(t, summaries, "bd-a", "bd-b", "bd-c")
}

func TestSortSummaries_Updated_NewestFirst(t *testing.T) {
	summaries := sortSummariesFixture()
	sortSummaries(summaries, "updated", false)
	assertIDOrder(t, summaries, "bd-b", "bd-c", "bd-a")
}

func TestSortSummaries_Closed_NonNilBeforeNil_NewestFirst(t *testing.T) {
	summaries := sortSummariesFixture()
	sortSummaries(summaries, "closed", false)
	// bd-a has closed=t0+90m, bd-c has closed=t0+30m, bd-b is open (nil).
	// Expected: newest closed first, nil last.
	assertIDOrder(t, summaries, "bd-a", "bd-c", "bd-b")
}

func TestSortSummaries_Closed_AllNil_Stable(t *testing.T) {
	s := []*types.IssueSummary{
		{ID: "bd-1", ClosedAt: nil},
		{ID: "bd-2", ClosedAt: nil},
	}
	sortSummaries(s, "closed", false)
	// Equal (both nil) — relative order preserved by slices.SortFunc stable contract
	// is NOT guaranteed, but both nil returning 0 means neither should be ranked
	// before the other. Assertion here is just that nothing panics and both
	// entries remain present.
	ids := summaryIDs(s)
	if len(ids) != 2 {
		t.Fatalf("expected 2 entries, got %v", ids)
	}
	seen := map[string]bool{"bd-1": false, "bd-2": false}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen["bd-1"] || !seen["bd-2"] {
		t.Fatalf("missing entry after sort: %v", ids)
	}
}

func TestSortSummaries_Status(t *testing.T) {
	summaries := sortSummariesFixture()
	sortSummaries(summaries, "status", false)
	// cmp.Compare on the underlying string value of Status.
	// Fixture: "closed" < "in_progress" < "open".
	assertIDOrder(t, summaries, "bd-c", "bd-b", "bd-a")
}

func TestSortSummaries_ID(t *testing.T) {
	summaries := sortSummariesFixture()
	sortSummaries(summaries, "id", false)
	assertIDOrder(t, summaries, "bd-a", "bd-b", "bd-c")
}

func TestSortSummaries_Title_CaseInsensitive(t *testing.T) {
	summaries := sortSummariesFixture()
	sortSummaries(summaries, "title", false)
	// Titles: Mango, apple, BANANA — case-folded: apple, banana, mango.
	assertIDOrder(t, summaries, "bd-a", "bd-c", "bd-b")
}

func TestSortSummaries_Type(t *testing.T) {
	summaries := sortSummariesFixture()
	sortSummaries(summaries, "type", false)
	// Types: task, bug, feature — alphabetical by underlying string.
	assertIDOrder(t, summaries, "bd-a", "bd-c", "bd-b")
}

func TestSortSummaries_Assignee(t *testing.T) {
	summaries := sortSummariesFixture()
	sortSummaries(summaries, "assignee", false)
	assertIDOrder(t, summaries, "bd-a", "bd-b", "bd-c")
}

func TestSortSummaries_Reverse_InvertsEveryField(t *testing.T) {
	// Each case runs the forward sort, then the reverse sort, and asserts the
	// reverse order is the exact reversal of the forward order. This guards
	// against a reverse-branch regression affecting only some fields.
	fields := []string{"priority", "created", "updated", "status", "id", "title", "type", "assignee"}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			fwd := sortSummariesFixture()
			sortSummaries(fwd, field, false)
			fwdIDs := summaryIDs(fwd)

			rev := sortSummariesFixture()
			sortSummaries(rev, field, true)
			revIDs := summaryIDs(rev)

			if len(fwdIDs) != len(revIDs) {
				t.Fatalf("length mismatch: fwd=%v rev=%v", fwdIDs, revIDs)
			}
			for i := range fwdIDs {
				if fwdIDs[i] != revIDs[len(revIDs)-1-i] {
					t.Fatalf("reverse sort by %q not reverse of forward: fwd=%v rev=%v", field, fwdIDs, revIDs)
				}
			}
		})
	}
}

func TestSortSummaries_Closed_ReverseFlipsNilPosition(t *testing.T) {
	// Mirrors the sortIssues contract: reverse=true negates every comparator
	// result, including the +1/-1 nil-handling branches, so nil-ClosedAt entries
	// migrate from the tail (reverse=false) to the head (reverse=true). This
	// is the same behavior as sortIssues — be-nu4.3 intentionally keeps parity.
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c1 := t0.Add(30 * time.Minute)
	c2 := t0.Add(90 * time.Minute)

	fwd := []*types.IssueSummary{
		{ID: "bd-open", ClosedAt: nil},
		{ID: "bd-oldclose", ClosedAt: &c1},
		{ID: "bd-newclose", ClosedAt: &c2},
	}
	sortSummaries(fwd, "closed", false)
	if fwd[len(fwd)-1].ID != "bd-open" {
		t.Fatalf("reverse=false: nil-ClosedAt must be last; got %v", summaryIDs(fwd))
	}

	rev := []*types.IssueSummary{
		{ID: "bd-open", ClosedAt: nil},
		{ID: "bd-oldclose", ClosedAt: &c1},
		{ID: "bd-newclose", ClosedAt: &c2},
	}
	sortSummaries(rev, "closed", true)
	if rev[0].ID != "bd-open" {
		t.Fatalf("reverse=true: nil-ClosedAt must be first; got %v", summaryIDs(rev))
	}
}

func TestSortSummaries_UnrecognizedField_IsNoOp(t *testing.T) {
	summaries := sortSummariesFixture()
	originalOrder := summaryIDs(summaries)
	sortSummaries(summaries, "not-a-real-field", false)
	// Default branch returns 0 for every pair, so slices.SortFunc must leave
	// the input unchanged (stable w.r.t. an all-equal comparator).
	got := summaryIDs(summaries)
	for i := range originalOrder {
		if got[i] != originalOrder[i] {
			t.Fatalf("unrecognized sort field must be a no-op; got %v want %v", got, originalOrder)
		}
	}
}

func TestSortSummaries_Empty(t *testing.T) {
	// Defensive: every case branch must survive an empty slice without panic.
	for _, field := range []string{"", "priority", "created", "updated", "closed", "status", "id", "title", "type", "assignee", "bogus"} {
		t.Run(field, func(t *testing.T) {
			var empty []*types.IssueSummary
			sortSummaries(empty, field, false)
			if len(empty) != 0 {
				t.Fatalf("expected empty slice to remain empty")
			}
		})
	}
}
