package dolt

import (
	"context"
	"slices"
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

// TestSearchIssuesAndSearchIssueSummaries_Parity asserts that SearchIssues and
// SearchIssueSummaries return the same ID set, in the same order, across the
// same representative filters TestSearchIssuesAndSearchIssueIDs_Parity checks
// above. SearchIssueSummaries shares the same generic searchInTx core
// (summaryProjection, internal/storage/issueops/search.go) as SearchIssues and
// SearchIssueIDs; this is that contract's third leg.
func TestSearchIssuesAndSearchIssueSummaries_Parity(t *testing.T) {
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
			summaries, err := store.SearchIssueSummaries(ctx, tc.query, tc.filter)
			if err != nil {
				t.Fatalf("SearchIssueSummaries: %v", err)
			}

			fromIssues := make([]string, len(issues))
			for i, issue := range issues {
				fromIssues[i] = issue.ID
			}
			fromSummaries := make([]string, len(summaries))
			for i, s := range summaries {
				fromSummaries[i] = s.ID
			}

			// Order-sensitive: both paths share the same ORDER BY, so unlike
			// equalStringSlices (which sorts before comparing and only proves
			// the same ID set), this must also catch drift in relative row order.
			if !slices.Equal(fromIssues, fromSummaries) {
				t.Errorf("SearchIssues vs SearchIssueSummaries disagree:\n  SearchIssues:         %v\n  SearchIssueSummaries: %v",
					fromIssues, fromSummaries)
			}
		})
	}
}

// TestSearchIssueSummaries_FieldsMatchSearchIssues asserts that every field
// types.IssueSummary carries agrees with the corresponding types.Issue field
// for the same row, including the nullable/derived fields most likely to
// silently drift from ScanIssueFrom if ScanIssueSummaryFrom's column list or
// scan order falls out of sync: Pinned, Labels, Assignee, and ClosedAt.
func TestSearchIssueSummaries_FieldsMatchSearchIssues(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	pinned := &types.Issue{
		ID:        "search-summary-pinned",
		Title:     "pinned reference issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		Assignee:  "alice",
		Pinned:    true,
	}
	if err := store.CreateIssue(ctx, pinned, "tester"); err != nil {
		t.Fatalf("CreateIssue (pinned): %v", err)
	}
	if err := store.AddLabel(ctx, pinned.ID, "needs-summary-field", "tester"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}

	const closedID = "search-summary-closed"
	closedIssue := &types.Issue{
		ID:        closedID,
		Title:     "closed reference issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeBug,
	}
	if err := store.CreateIssue(ctx, closedIssue, "tester"); err != nil {
		t.Fatalf("CreateIssue (closed): %v", err)
	}
	if err := store.CloseIssue(ctx, closedID, "done", "tester", "s1"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	summaries, err := store.SearchIssueSummaries(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssueSummaries: %v", err)
	}
	if len(summaries) != len(issues) {
		t.Fatalf("SearchIssueSummaries returned %d rows, SearchIssues returned %d", len(summaries), len(issues))
	}

	byID := make(map[string]*types.Issue, len(issues))
	for _, issue := range issues {
		byID[issue.ID] = issue
	}

	for _, s := range summaries {
		issue, ok := byID[s.ID]
		if !ok {
			t.Fatalf("SearchIssueSummaries returned %q, not present in SearchIssues result", s.ID)
		}
		if s.Title != issue.Title {
			t.Errorf("%s: Title = %q, want %q", s.ID, s.Title, issue.Title)
		}
		if s.Status != issue.Status {
			t.Errorf("%s: Status = %q, want %q", s.ID, s.Status, issue.Status)
		}
		if s.Priority != issue.Priority {
			t.Errorf("%s: Priority = %d, want %d", s.ID, s.Priority, issue.Priority)
		}
		if s.IssueType != issue.IssueType {
			t.Errorf("%s: IssueType = %q, want %q", s.ID, s.IssueType, issue.IssueType)
		}
		if s.Assignee != issue.Assignee {
			t.Errorf("%s: Assignee = %q, want %q", s.ID, s.Assignee, issue.Assignee)
		}
		if s.Pinned != issue.Pinned {
			t.Errorf("%s: Pinned = %v, want %v", s.ID, s.Pinned, issue.Pinned)
		}
		if !equalStringSlices(s.Labels, issue.Labels) {
			t.Errorf("%s: Labels = %v, want %v", s.ID, s.Labels, issue.Labels)
		}
		if !s.CreatedAt.Equal(issue.CreatedAt) {
			t.Errorf("%s: CreatedAt = %v, want %v", s.ID, s.CreatedAt, issue.CreatedAt)
		}
		if !s.UpdatedAt.Equal(issue.UpdatedAt) {
			t.Errorf("%s: UpdatedAt = %v, want %v", s.ID, s.UpdatedAt, issue.UpdatedAt)
		}
		switch {
		case s.ClosedAt == nil && issue.ClosedAt == nil:
			// both nil: fine
		case s.ClosedAt == nil || issue.ClosedAt == nil:
			t.Errorf("%s: ClosedAt nil-ness mismatch: summary=%v issue=%v", s.ID, s.ClosedAt, issue.ClosedAt)
		case !s.ClosedAt.Equal(*issue.ClosedAt):
			t.Errorf("%s: ClosedAt = %v, want %v", s.ID, *s.ClosedAt, *issue.ClosedAt)
		}
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
