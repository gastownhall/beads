package dolt

import (
	"context"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestSearchIssuesAndSearchIssueIDs_Parity asserts that SearchIssues and
// SearchIssueIDs return the same ID set across a representative range of
// filters. The two APIs share a generic core in issueops/search.go; this
// test is the contract that catches any future drift if a contributor adds
// a behavior to one path but not the other.
func TestSearchIssuesAndSearchIssueIDs_Parity(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	seedSearchParityFixture(ctx, t, store)

	cases := []struct {
		name   string
		query  string
		filter types.IssueFilter
	}{
		{name: "no filter, full set"},
		{name: "open status only", filter: types.IssueFilter{Statuses: []types.Status{types.StatusOpen}}},
		{name: "priority filter", filter: types.IssueFilter{Priority: ptr(1)}},
		{name: "substring search (id_parser fallback)", query: "search-parity"},
		{name: "substring search no match", query: "no-such-prefix-anywhere"},
		{name: "label-driven (DISTINCT join)", filter: types.IssueFilter{Labels: []string{"alpha"}}},
		{name: "ephemeral only", filter: types.IssueFilter{Ephemeral: ptr(true)}},
		{name: "non-ephemeral only", filter: types.IssueFilter{Ephemeral: ptr(false)}},
		{name: "limit applied", filter: types.IssueFilter{Limit: 2}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			issues, err := store.SearchIssues(ctx, tc.query, tc.filter)
			if err != nil {
				t.Fatalf("SearchIssues: %v", err)
			}
			ids, err := store.SearchIssueIDs(ctx, tc.query, tc.filter)
			if err != nil {
				t.Fatalf("SearchIssueIDs: %v", err)
			}

			fromIssues := make([]string, len(issues))
			for i, issue := range issues {
				fromIssues[i] = issue.ID
			}

			// Limit only constrains *count* — ordering between the two paths
			// must still agree because both use the same ORDER BY. So compare
			// in-order, not as sets.
			if !equalStringSlices(fromIssues, ids) {
				t.Errorf("SearchIssues vs SearchIssueIDs disagree:\n  SearchIssues:   %v\n  SearchIssueIDs: %v",
					fromIssues, ids)
			}
		})
	}
}

func seedSearchParityFixture(ctx context.Context, t *testing.T, store *DoltStore) {
	t.Helper()

	// Persistent issues spanning status, priority, and labels.
	issues := []*types.Issue{
		{ID: "search-parity-a", Title: "alpha", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "search-parity-b", Title: "beta", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "search-parity-c", Title: "gamma", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
		{ID: "search-parity-d", Title: "delta", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask},
	}
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	if err := store.CloseIssue(ctx, "search-parity-d", "done", "tester", "s1"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	if err := store.AddLabel(ctx, "search-parity-a", "alpha", "tester"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if err := store.AddLabel(ctx, "search-parity-c", "alpha", "tester"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}

	// One wisp to exercise the wisp-merge path.
	wisp := &types.Issue{Title: "search-parity wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue (wisp): %v", err)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	// Tolerate ordering differences caused by ties in the ORDER BY (priority,
	// created_at, id). Sort defensively before compare — the structural claim
	// is "same IDs," not "same physical row order."
	ax := append([]string(nil), a...)
	bx := append([]string(nil), b...)
	sort.Strings(ax)
	sort.Strings(bx)
	for i := range ax {
		if ax[i] != bx[i] {
			return false
		}
	}
	return true
}

func ptr[T any](v T) *T { return &v }
