package workapi

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestReadyListingOf pins the one definition both ReadyLister bodies finish
// through: has-more is read off the total, only a limited page can hide rows,
// and the total never drops below the rows the page itself proves exist.
func TestReadyListingOf(t *testing.T) {
	rows := func(n int) []*types.IssueWithCounts {
		out := make([]*types.IssueWithCounts, n)
		for i := range out {
			out[i] = &types.IssueWithCounts{Issue: &types.Issue{}}
		}
		return out
	}
	cases := []struct {
		name      string
		items     int
		offset    int
		limit     int
		total     int64
		wantMore  bool
		wantTotal int64
	}{
		{"truncated", 2, 0, 2, 5, true, 5},
		{"exact", 5, 0, 5, 5, false, 5},
		{"unlimited never has more", 5, 0, 0, 9, false, 9},
		{"offset middle", 2, 2, 2, 5, true, 5},
		{"offset last", 1, 4, 2, 5, false, 5},
		{"past the end keeps the total", 0, 9, 2, 5, false, 5},
		{"stale total is floored to the page", 3, 2, 3, 4, false, 5},
		{"empty", 0, 0, 1, 0, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReadyListingOf(rows(tc.items), tc.offset, tc.limit, tc.total)
			if got.HasMore != tc.wantMore || got.Total != tc.wantTotal || len(got.Items) != tc.items {
				t.Errorf("ReadyListingOf = more=%v total=%d items=%d, want more=%v total=%d items=%d",
					got.HasMore, got.Total, len(got.Items), tc.wantMore, tc.wantTotal, tc.items)
			}
		})
	}
	if got := ReadyListingOf(nil, 0, 1, 0); got.Items == nil {
		t.Error("ReadyListingOf(nil) Items = nil, want a non-nil empty page")
	}
}
