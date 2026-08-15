package workapi

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func pageRows(ids ...string) []*types.IssueWithCounts {
	out := make([]*types.IssueWithCounts, 0, len(ids))
	for i, id := range ids {
		out = append(out, &types.IssueWithCounts{Issue: &types.Issue{
			ID:        id,
			Priority:  i,
			CreatedAt: time.Unix(int64(1000-i), 0),
		}})
	}
	return out
}

func pageIDs[T PageRow](rows []T) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowIssue(row).ID)
	}
	return out
}

// TestFinishPageIsTheOneEpilogue exercises the tail every list and ready read
// now shares. Each case is a difference that has actually shipped between two
// implementations of it.
func TestFinishPageIsTheOneEpilogue(t *testing.T) {
	tests := []struct {
		name    string
		rows    []*types.IssueWithCounts
		sortBy  string
		reverse bool
		limit   int
		hasMore bool

		wantIDs []string
		wantHas bool
	}{{
		// The over-fetch case: the seam reports nothing, the extra row does.
		name:    "an over-fetched row is the has-more answer",
		rows:    pageRows("a", "b", "c"),
		limit:   2,
		wantIDs: []string{"a", "b"},
		wantHas: true,
	}, {
		// The native case: the seam already knows, and no row is spare.
		name:    "a seam that reports has-more is believed",
		rows:    pageRows("a", "b"),
		limit:   2,
		hasMore: true,
		wantIDs: []string{"a", "b"},
		wantHas: true,
	}, {
		name:    "an unlimited page is never cut",
		rows:    pageRows("a", "b", "c"),
		limit:   0,
		wantIDs: []string{"a", "b", "c"},
		wantHas: false,
	}, {
		// The cut is the only thing this function can ADD to the verdict, so
		// with no cut the seam's answer passes through untouched. An
		// unlimited request that the seam bounded anyway must not come back
		// reported as complete — which is what "0 means there is by
		// definition nothing more" would have meant.
		name:    "an unlimited page still carries the seam's verdict",
		rows:    pageRows("a", "b", "c"),
		limit:   0,
		hasMore: true,
		wantIDs: []string{"a", "b", "c"},
		wantHas: true,
	}, {
		// The bug this ordering exists to prevent: a sort SQL cannot express
		// leaves the query unlimited, so the cut must run AFTER the order or
		// it keeps the wrong rows.
		name:    "the sort decides which rows the cut keeps",
		rows:    pageRows("a", "b", "c"),
		sortBy:  "id",
		reverse: true,
		limit:   2,
		wantIDs: []string{"c", "b"},
		wantHas: true,
	}, {
		name:    "an empty order leaves storage's own order alone",
		rows:    pageRows("c", "a", "b"),
		limit:   0,
		wantIDs: []string{"c", "a", "b"},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, hasMore := FinishPage(tt.rows, tt.sortBy, tt.reverse, tt.limit, tt.hasMore)
			if ids := pageIDs(got); !equalStrings(ids, tt.wantIDs) {
				t.Errorf("ids = %v, want %v", ids, tt.wantIDs)
			}
			if hasMore != tt.wantHas {
				t.Errorf("hasMore = %v, want %v", hasMore, tt.wantHas)
			}
		})
	}
}

// TestFinishPageNeverAnswersNull pins the property every surface that
// serializes a page depends on: a caller must not have to tell null from empty
// to learn that nothing matched.
func TestFinishPageNeverAnswersNull(t *testing.T) {
	counted, _ := FinishPage[*types.IssueWithCounts](nil, "", false, 10, false)
	if counted == nil {
		t.Error("a counted page came back nil")
	}
	issues, _ := FinishPage[*types.Issue](nil, "priority", false, 0, false)
	if issues == nil {
		t.Error("an issue page came back nil")
	}
}

// TestFinishPageSortsBothRowShapes is the reason the epilogue is generic: the
// text renderings page []*types.Issue and every --json and wire body pages
// []*types.IssueWithCounts, and one drifting from the other is what a single
// function makes impossible.
func TestFinishPageSortsBothRowShapes(t *testing.T) {
	counted := pageRows("b", "a", "c")
	gotCounted, _ := FinishPage(counted, "id", false, 0, false)

	bare := []*types.Issue{{ID: "b"}, {ID: "a"}, {ID: "c"}}
	gotBare, _ := FinishPage(bare, "id", false, 0, false)

	if !equalStrings(pageIDs(gotCounted), pageIDs(gotBare)) {
		t.Errorf("the two row shapes ordered differently: counted %v, bare %v",
			pageIDs(gotCounted), pageIDs(gotBare))
	}
}

// TestFinishPageToleratesAHollowRow guards the one nil shape storage can hand
// back: a counted row whose issue never loaded. Ordering must not panic on it.
func TestFinishPageToleratesAHollowRow(t *testing.T) {
	rows := []*types.IssueWithCounts{{Issue: &types.Issue{ID: "b"}}, {}, nil}
	got, _ := FinishPage(rows, "id", false, 0, false)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0] == nil || got[0].Issue == nil || got[0].Issue.ID != "b" {
		t.Errorf("the real row did not sort first: %#v", got[0])
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
