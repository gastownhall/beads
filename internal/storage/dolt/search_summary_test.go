package dolt

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestSearchIssueSummaries_IDParity covers the be-nu4.3.2 ID-parity hard gate.
// For every IssueFilter field that's meaningful against a seeded 1K-row
// fixture, SearchIssueSummaries must return the same IDs as SearchIssues in
// the same order (both paths share the ORDER BY priority, created_at DESC, id
// clause, so any divergence signals filter-clause or wisp-admission drift).
func TestSearchIssueSummaries_IDParity(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	seedSummaryParityFixture(t, store, 1000)

	open := types.StatusOpen
	inProgress := types.StatusInProgress
	closed := types.StatusClosed
	priority1 := 1
	pinnedTrue := true
	ephemeralTrue := true
	ephemeralFalse := false
	task := types.TypeTask
	bug := types.TypeBug
	assignee3 := "user-3"

	cases := []struct {
		name   string
		filter types.IssueFilter
	}{
		{"no_filter", types.IssueFilter{}},
		{"status_open", types.IssueFilter{Status: &open}},
		{"status_in_progress", types.IssueFilter{Status: &inProgress}},
		{"status_closed", types.IssueFilter{Status: &closed}},
		{"priority_1", types.IssueFilter{Priority: &priority1}},
		{"type_task", types.IssueFilter{IssueType: &task}},
		{"type_bug", types.IssueFilter{IssueType: &bug}},
		{"assignee", types.IssueFilter{Assignee: &assignee3}},
		{"label_all", types.IssueFilter{Labels: []string{"perf"}}},
		{"label_any", types.IssueFilter{LabelsAny: []string{"perf", "storage"}}},
		{"pinned_only", types.IssueFilter{Pinned: &pinnedTrue}},
		{"ephemeral_true", types.IssueFilter{Ephemeral: &ephemeralTrue}},
		{"ephemeral_false", types.IssueFilter{Ephemeral: &ephemeralFalse}},
		{"limit_20", types.IssueFilter{Limit: 20}},
		{"title_contains", types.IssueFilter{TitleContains: "summary"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues, err := store.SearchIssues(ctx, "", tc.filter)
			if err != nil {
				t.Fatalf("SearchIssues: %v", err)
			}
			summaries, err := store.SearchIssueSummaries(ctx, "", tc.filter)
			if err != nil {
				t.Fatalf("SearchIssueSummaries: %v", err)
			}

			if len(issues) != len(summaries) {
				t.Fatalf("count mismatch: SearchIssues=%d, SearchIssueSummaries=%d",
					len(issues), len(summaries))
			}
			for i := range issues {
				if issues[i].ID != summaries[i].ID {
					t.Errorf("ID order mismatch at %d: issues=%q summaries=%q",
						i, issues[i].ID, summaries[i].ID)
				}
			}
		})
	}
}

// TestSearchIssueSummaries_PinnedFixturePresent asserts the fixture contains
// both a pinned permanent issue AND a pinned wisp. The render-parity hard gate
// (exercised in cmd/bd/list_format_test.go) relies on these being present
// so pinIndicator / pinIndicatorSummary both run. Losing the pinned wisp in
// the fixture would silently weaken render parity coverage.
func TestSearchIssueSummaries_PinnedFixturePresent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	seedSummaryParityFixture(t, store, 1000)

	summaries, err := store.SearchIssueSummaries(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssueSummaries: %v", err)
	}
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}

	var pinnedSumPerm, pinnedSumWisp bool
	for _, s := range summaries {
		if !s.Pinned {
			continue
		}
		// Perms come from CreateIssues with explicit ID prefix par-perm-;
		// wisps are created one-by-one with generated IDs in the wisps table.
		if len(s.ID) >= len("par-perm-") && s.ID[:len("par-perm-")] == "par-perm-" {
			pinnedSumPerm = true
		} else {
			pinnedSumWisp = true
		}
	}
	if !pinnedSumPerm {
		t.Error("fixture missing pinned permanent issue (summary path)")
	}
	if !pinnedSumWisp {
		t.Error("fixture missing pinned wisp (summary path)")
	}

	// Cross-check: the SearchIssues path must see the same pinned shape, or
	// the fixture itself is broken rather than a summary-side regression.
	var pinnedIssPerm, pinnedIssWisp bool
	for _, iss := range issues {
		if !iss.Pinned {
			continue
		}
		if iss.Ephemeral {
			pinnedIssWisp = true
		} else {
			pinnedIssPerm = true
		}
	}
	if pinnedIssPerm != pinnedSumPerm || pinnedIssWisp != pinnedSumWisp {
		t.Errorf("pinned-shape divergence: issues(perm=%t, wisp=%t) summaries(perm=%t, wisp=%t)",
			pinnedIssPerm, pinnedIssWisp, pinnedSumPerm, pinnedSumWisp)
	}
}

// seedSummaryParityFixture populates the store with `n` issues distributed
// across statuses / priorities / types / assignees, labels on a subset, and
// two intentionally-pinned items: one permanent issue and one wisp. Returns
// nothing — callers query the store after the fixture is built.
func seedSummaryParityFixture(t *testing.T, store *DoltStore, n int) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()

	numWisps := n / 4
	numPerms := n - numWisps
	statuses := []types.Status{types.StatusOpen, types.StatusInProgress, types.StatusClosed}
	issueTypes := []types.IssueType{types.TypeTask, types.TypeBug, types.TypeFeature, types.TypeEpic}
	labels := [][]string{nil, {"perf"}, {"storage"}, {"perf", "storage"}}

	const batch = 200
	for start := 0; start < numPerms; start += batch {
		end := start + batch
		if end > numPerms {
			end = numPerms
		}
		chunk := make([]*types.Issue, 0, end-start)
		for i := start; i < end; i++ {
			iss := &types.Issue{
				ID:        fmt.Sprintf("par-perm-%04d", i),
				Title:     fmt.Sprintf("parity summary perm %04d", i),
				Status:    statuses[i%len(statuses)],
				Priority:  i % 5,
				IssueType: issueTypes[i%len(issueTypes)],
				Assignee:  fmt.Sprintf("user-%d", i%5),
			}
			if i == 7 {
				iss.Pinned = true
				iss.Title = "parity summary perm 0007 (pinned)"
			}
			chunk = append(chunk, iss)
		}
		if err := store.CreateIssues(ctx, chunk, "test"); err != nil {
			t.Fatalf("create perms: %v", err)
		}
		for i, iss := range chunk {
			for _, lb := range labels[(start+i)%len(labels)] {
				if err := store.AddLabel(ctx, iss.ID, lb, "test"); err != nil {
					t.Fatalf("add label %q to %s: %v", lb, iss.ID, err)
				}
			}
		}
	}

	for i := 0; i < numWisps; i++ {
		iss := &types.Issue{
			Title:     fmt.Sprintf("parity summary wisp %04d", i),
			Status:    types.StatusOpen,
			Priority:  i % 5,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if i == 3 {
			iss.Pinned = true
			iss.Title = "parity summary wisp 0003 (pinned)"
		}
		if err := store.CreateIssue(ctx, iss, "test"); err != nil {
			t.Fatalf("create wisp %d: %v", i, err)
		}
	}
}
