package dolt

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestGetReadyWork_DeferredParentVariantsHoldChildren covers every parent
// shape that can hold a direct child back from ready work (GH#7300): a
// status-deferred parent with no date, and a non-deferred parent with a
// future defer date. An ordinary open parent holds nothing, and its child
// stays ready.
func TestGetReadyWork_DeferredParentVariantsHoldChildren(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	create := func(parentID, childID string) {
		t.Helper()
		issues := []*types.Issue{
			{ID: parentID, Title: parentID, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic},
			{ID: childID, Title: childID, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		}
		for _, iss := range issues {
			if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
				t.Fatalf("create %s: %v", iss.ID, err)
			}
		}
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: childID, DependsOnID: parentID, Type: types.DepParentChild,
		}, "tester"); err != nil {
			t.Fatalf("add dependency %s -> %s: %v", childID, parentID, err)
		}
	}

	// A parent deferred with status only: bd defer <id> without --until.
	create("rw-var-undated-epic", "rw-var-undated-child")
	if err := store.UpdateIssue(ctx, "rw-var-undated-epic", map[string]interface{}{
		"status": string(types.StatusDeferred),
	}, "tester"); err != nil {
		t.Fatalf("defer undated epic: %v", err)
	}
	// An open parent with only a future defer date.
	create("rw-var-dated-epic", "rw-var-dated-child")
	if err := store.UpdateIssue(ctx, "rw-var-dated-epic", map[string]interface{}{
		"defer_until": time.Now().UTC().Add(24 * time.Hour),
	}, "tester"); err != nil {
		t.Fatalf("snooze open epic: %v", err)
	}
	// An ordinary open parent: its child must stay ready.
	create("rw-var-open-epic", "rw-var-open-child")

	work, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	ids := issueIDs(work)
	if containsID(ids, "rw-var-undated-child") {
		t.Errorf("child of the status-deferred parent is ready: %v", ids)
	}
	if containsID(ids, "rw-var-dated-child") {
		t.Errorf("child of the future-dated parent is ready: %v", ids)
	}
	if !containsID(ids, "rw-var-open-child") {
		t.Errorf("child of the ordinary open parent is not ready: %v", ids)
	}
}