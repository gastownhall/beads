//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func ephemeralOnly() *bool { b := true; return &b }

func searchIDs(t *testing.T, te *testEnv, filter types.IssueFilter) ([]string, error) {
	t.Helper()
	got, err := te.store.SearchIssues(t.Context(), "", filter)
	ids := make([]string, 0, len(got))
	for _, issue := range got {
		ids = append(ids, issue.ID)
	}
	return ids, err
}

// TestSearchWithMissingWispPlaneTables pins which missing tables a wisp search
// may treat as "no wisps" and which it must report. The wisp query and its
// label hydration span more tables than the wisp plane owns, and folding all
// of them into an empty result set drops live rows from an ordinary list with
// no error to show for it.
func TestSearchWithMissingWispPlaneTables(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	seed := func(t *testing.T, te *testEnv, prefix string) {
		t.Helper()
		wisp := &types.Issue{
			ID: prefix + "-wisp", Title: "ephemeral bead", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		}
		if err := te.store.CreateIssue(t.Context(), wisp, "tester"); err != nil {
			t.Fatalf("CreateIssue wisp: %v", err)
		}
		durable := &types.Issue{
			ID: prefix + "-issue", Title: "durable bead", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(t.Context(), durable, "tester"); err != nil {
			t.Fatalf("CreateIssue durable: %v", err)
		}
	}

	// A missing wisp_labels is not the wisp plane being absent, it is the wisp
	// plane being broken. assertRowExists is the control: the wisp is present
	// while the search is answering that there is nothing there.
	t.Run("missing_wisp_labels_is_an_error_not_an_empty_result", func(t *testing.T) {
		te := newTestEnv(t, "wl2")
		seed(t, te, "wl2")
		te.exec(t, t.Context(), "RENAME TABLE wisp_labels TO wisp_labels_renamed_away")
		te.assertRowExists(t, t.Context(), "wisps", "wl2-wisp")

		if ids, err := searchIDs(t, te, types.IssueFilter{Ephemeral: ephemeralOnly()}); err == nil {
			t.Fatalf("ephemeral search returned %v with no error and no wisp_labels table", ids)
		}
		if ids, err := searchIDs(t, te, types.IssueFilter{}); err == nil {
			t.Fatalf("merged search returned %v with no error and no wisp_labels table", ids)
		}
	})

	// The control the tolerance exists for: a pre-migration database has no
	// wisp plane at all, and a search of it really does match no wisps.
	t.Run("missing_wisps_table_is_an_empty_result", func(t *testing.T) {
		te := newTestEnv(t, "wt2")
		seed(t, te, "wt2")
		te.exec(t, t.Context(), "RENAME TABLE wisps TO wisps_renamed_away")

		ids, err := searchIDs(t, te, types.IssueFilter{Ephemeral: ephemeralOnly()})
		if err != nil {
			t.Fatalf("ephemeral search on a database with no wisp plane: %v", err)
		}
		if len(ids) != 0 {
			t.Fatalf("ephemeral search returned %v, want none", ids)
		}

		ids, err = searchIDs(t, te, types.IssueFilter{})
		if err != nil {
			t.Fatalf("merged search on a database with no wisp plane: %v", err)
		}
		if len(ids) != 1 || ids[0] != "wt2-issue" {
			t.Fatalf("merged search returned %v, want just the durable issue", ids)
		}
	})

	// The second control: wisp_dependencies is genuinely optional and its
	// absence must stay invisible to a plain search.
	//
	// It does not pin the tolerance set, and it is not meant to: a search that
	// does not ask for dependencies never reads the table, so this stays green
	// whatever missingOptionalWispTable is tightened to. What pins the
	// wisp_dependencies entry is the sqlmock control in
	// issueops/search_missing_table_test.go, which drives the failure through
	// the guard directly. This one proves the end-to-end query still answers.
	t.Run("missing_wisp_dependencies_is_invisible", func(t *testing.T) {
		te := newTestEnv(t, "wd2")
		seed(t, te, "wd2")
		te.exec(t, t.Context(), "RENAME TABLE wisp_dependencies TO wisp_dependencies_renamed_away")

		ids, err := searchIDs(t, te, types.IssueFilter{Ephemeral: ephemeralOnly()})
		if err != nil {
			t.Fatalf("ephemeral search with no wisp_dependencies: %v", err)
		}
		if len(ids) != 1 || ids[0] != "wd2-wisp" {
			t.Fatalf("ephemeral search returned %v, want just the wisp", ids)
		}
	})
}
