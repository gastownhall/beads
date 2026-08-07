//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestReadyWorkExcludesHumanLabelledIssues is sk-1pc's persistence-level
// guard. A bead labeled 'human' is queued for an operator ruling — it appears
// in `bd human list` — but the label used not to remove it from ready work, so
// the same bead sat in the decision queue and the dispatch queue at once and a
// worker could claim and work a question that was still awaiting an answer.
//
// The unit coverage over the clause text lives in
// sqlbuild.TestBuildReadyWorkWhereExcludesHumanLabel; this runs the real
// query, over a real store, through both ready entry points, because the
// clause has to survive the label JOIN and the issues/wisps merge to matter.
func TestReadyWorkExcludesHumanLabelledIssues(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "rh")
	ctx := t.Context()

	for _, issue := range []*types.Issue{
		{ID: "rh-workable", Title: "workable", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "rh-awaiting", Title: "awaiting an operator", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
	}
	if err := te.store.AddLabel(ctx, "rh-awaiting", "human", "tester"); err != nil {
		t.Fatalf("AddLabel human: %v", err)
	}
	// A near miss is an ordinary label: only the exact spelling is the
	// operator queue, and only the exact spelling is excluded.
	if err := te.store.AddLabel(ctx, "rh-workable", "needs-human", "tester"); err != nil {
		t.Fatalf("AddLabel needs-human: %v", err)
	}

	readyIDs := func(t *testing.T, filter types.WorkFilter) []string {
		t.Helper()
		issues, err := te.store.GetReadyWork(ctx, filter)
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		ids := make([]string, 0, len(issues))
		for _, issue := range issues {
			ids = append(ids, issue.ID)
		}
		return ids
	}
	contains := func(ids []string, want string) bool {
		for _, id := range ids {
			if id == want {
				return true
			}
		}
		return false
	}

	t.Run("default ready work hides the human-labelled bead", func(t *testing.T) {
		ids := readyIDs(t, types.WorkFilter{})
		if contains(ids, "rh-awaiting") {
			t.Errorf("rh-awaiting is awaiting an operator decision and must not be ready work: %v", ids)
		}
		if !contains(ids, "rh-workable") {
			t.Errorf("rh-workable must still be ready work: %v", ids)
		}
	})

	t.Run("the counts entry point agrees", func(t *testing.T) {
		withCounts, err := te.store.GetReadyWorkWithCounts(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWorkWithCounts: %v", err)
		}
		for _, issue := range withCounts {
			if issue.ID == "rh-awaiting" {
				t.Errorf("rh-awaiting must not be ready work on the counts path either")
			}
		}
	})

	t.Run("asking for the label by name opts back in", func(t *testing.T) {
		ids := readyIDs(t, types.WorkFilter{Labels: []string{"human"}})
		if !contains(ids, "rh-awaiting") {
			t.Errorf("--label human must return the human queue, not an empty set: %v", ids)
		}
	})

	t.Run("clearing the label makes the bead workable again", func(t *testing.T) {
		if err := te.store.RemoveLabel(ctx, "rh-awaiting", "human", "tester"); err != nil {
			t.Fatalf("RemoveLabel: %v", err)
		}
		ids := readyIDs(t, types.WorkFilter{})
		if !contains(ids, "rh-awaiting") {
			t.Errorf("rh-awaiting must be ready work once the operator has released it: %v", ids)
		}
	})
}
