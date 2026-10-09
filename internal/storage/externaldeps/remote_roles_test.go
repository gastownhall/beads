package externaldeps

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func pageOf(hasMore bool, ids ...string) issueops.IssuePage {
	items := make([]*issueops.IssueWithCounts, 0, len(ids))
	for _, id := range ids {
		items = append(items, &issueops.IssueWithCounts{Issue: &types.Issue{ID: id}})
	}
	return issueops.IssuePage{Items: items, HasMore: hasMore}
}

func idsOf(page issueops.IssuePage) string {
	ids := make([]string, 0, len(page.Items))
	for _, row := range page.Items {
		ids = append(ids, row.ID)
	}
	return strings.Join(ids, ",")
}

// TestExcludeAndPage pins the client-side half of the remote ready
// exclusion: drop the blocked rows from the widened window, then cut the
// caller's own page, with an honest HasMore.
func TestExcludeAndPage(t *testing.T) {
	blocked := map[string]bool{"b1": true, "b2": true}
	for _, tc := range []struct {
		name        string
		window      issueops.IssuePage
		offset      int
		limit       int
		wantIDs     string
		wantHasMore bool
	}{
		{"blocked rows leave the page full", pageOf(false, "b1", "a", "b2", "c", "d"), 0, 2, "a,c", true},
		{"offset counts unblocked rows only", pageOf(false, "a", "b1", "c", "d"), 1, 2, "c,d", false},
		{"unlimited keeps every unblocked row", pageOf(false, "a", "b1", "c"), 0, 0, "a,c", false},
		{"a server-truncated window keeps HasMore", pageOf(true, "a", "b1"), 0, 5, "a", true},
		{"an offset past the end is an empty page", pageOf(false, "a"), 3, 2, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := excludeAndPage(tc.window, blocked, tc.offset, tc.limit)
			if idsOf(got) != tc.wantIDs || got.HasMore != tc.wantHasMore {
				t.Errorf("got %q hasMore=%v, want %q hasMore=%v", idsOf(got), got.HasMore, tc.wantIDs, tc.wantHasMore)
			}
			if got.Items == nil {
				t.Error("an empty page must be an empty slice, not nil")
			}
		})
	}
}

func TestWidenedLimit(t *testing.T) {
	if got := *widenedLimit(3, 10, 2); got != 15 {
		t.Errorf("widenedLimit(3,10,2) = %d, want 15", got)
	}
	if got := *widenedLimit(3, 0, 2); got != 0 {
		t.Errorf("an unlimited read stays unlimited, got %d", got)
	}
}
