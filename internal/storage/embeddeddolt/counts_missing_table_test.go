//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// seedCountsPlane puts one durable issue and one wisp in the store so both
// legs of every count have something to find. The wisp is what keeps the
// wisps-table probe from short-circuiting the merge, so the query that reads
// the rest of the wisp plane is actually reached.
func seedCountsPlane(t *testing.T, te *testEnv, prefix string) {
	t.Helper()
	wisp := &types.Issue{
		ID:        prefix + "-wisp",
		Title:     "ephemeral bead",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := te.store.CreateIssue(t.Context(), wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}
	durable := &types.Issue{
		ID:        prefix + "-issue",
		Title:     "durable bead",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := te.store.CreateIssue(t.Context(), durable, "tester"); err != nil {
		t.Fatalf("CreateIssue durable: %v", err)
	}
}

// TestCountsWithMissingWispPlaneTables is the end-to-end twin of
// TestSearchWithMissingWispPlaneTables for the counting and ready-work reads.
// It runs against a real database rather than a mock, so it pins one of the
// tables named in the sqlmock guards -- wisp_labels, the only one this test
// renames -- as a table these queries genuinely read: renaming it away is
// enough to break a counted read, and the blanket tolerance answered that
// broken database with a durable-only number and no error. The other names in
// the guards (leases, wisp_comments) are argued from the query builders, not
// pinned end-to-end here; see the PR's Limitations.
func TestCountsWithMissingWispPlaneTables(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	// wisp_labels is not a wisp-plane table a database may legitimately lack:
	// a store that has wisps but no wisp_labels is broken, and every count
	// below reads it -- the counts mega-query hydrates labels, and grouping by
	// label queries it directly.
	t.Run("missing_wisp_labels_is_an_error", func(t *testing.T) {
		te := newTestEnv(t, "cwl")
		seedCountsPlane(t, te, "cwl")
		te.assertRowExists(t, t.Context(), "wisps", "cwl-wisp")
		te.exec(t, t.Context(), "RENAME TABLE wisp_labels TO wisp_labels_renamed_away")

		if got, err := te.store.SearchIssuesWithCounts(t.Context(), "", types.IssueFilter{}); err == nil {
			t.Errorf("SearchIssuesWithCounts hid a broken wisp plane, returning %d rows", len(got))
		}
		if got, err := te.store.GetReadyWorkWithCounts(t.Context(), types.WorkFilter{IncludeDeferred: true}); err == nil {
			t.Errorf("GetReadyWorkWithCounts hid a broken wisp plane, returning %d rows", len(got))
		}
		if got, err := te.store.CountIssuesByGroup(t.Context(), types.IssueFilter{}, "label"); err == nil {
			t.Errorf("CountIssuesByGroup(label) hid a broken wisp plane, returning %v", got)
		}
	})

	// The control the tightened guard must keep: a pre-migration database has
	// no wisps table at all, and a count over it really does have no wisps to
	// add. Over-tightening to "never tolerate" passes every assertion above
	// and fails here.
	t.Run("missing_wisps_table_is_a_durable_only_answer", func(t *testing.T) {
		te := newTestEnv(t, "cwt")
		seedCountsPlane(t, te, "cwt")
		te.exec(t, t.Context(), "RENAME TABLE wisps TO wisps_renamed_away")

		n, err := te.store.CountIssues(t.Context(), "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("CountIssues on a database with no wisp plane: %v", err)
		}
		if n != 1 {
			t.Fatalf("CountIssues = %d, want 1 (the durable issue)", n)
		}
		counts, err := te.store.CountIssuesByGroup(t.Context(), types.IssueFilter{}, "label")
		if err != nil {
			t.Fatalf("CountIssuesByGroup(label) on a database with no wisp plane: %v", err)
		}
		if counts["(no labels)"] != 1 {
			t.Fatalf("CountIssuesByGroup(label) = %v, want one unlabelled durable issue", counts)
		}
	})

	// The second control: wisp_dependencies is genuinely optional and its
	// absence must stay invisible to a plain count.
	//
	// This does not pin the tolerance set, and is not meant to: a plain
	// COUNT(*) over wisps never reads the dependency table, so it stays green
	// whatever missingOptionalWispTable is tightened to. What pins the
	// wisp_dependencies entry is the sqlmock control in
	// issueops/counts_missing_table_test.go, which drives the failure through
	// the guard directly. This one proves the end-to-end count still answers.
	t.Run("missing_wisp_dependencies_is_invisible", func(t *testing.T) {
		te := newTestEnv(t, "cwd")
		seedCountsPlane(t, te, "cwd")
		te.exec(t, t.Context(), "RENAME TABLE wisp_dependencies TO wisp_dependencies_renamed_away")

		n, err := te.store.CountIssues(t.Context(), "", types.IssueFilter{})
		if err != nil {
			t.Fatalf("CountIssues with no wisp_dependencies table: %v", err)
		}
		if n != 2 {
			t.Fatalf("CountIssues = %d, want 2 (the durable issue and the wisp)", n)
		}
	})
}
