package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestGetReadyWork_IncludeDeferredBypassesUndatedDeferredParentHold pins the
// --include-deferred bypass for an undated deferred parent (GH#7300): the flag
// lifts the parent hold and lists and counts the child again, while the
// parent's own status still keeps the parent out of the ready set.
func TestGetReadyWork_IncludeDeferredBypassesUndatedDeferredParentHold(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	parent := &types.Issue{ID: "rw-idf-udp-epic", Title: "Undated epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
	child := &types.Issue{ID: "rw-idf-udp-child", Title: "Child of undated epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	for _, iss := range []*types.Issue{parent, child} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", iss.ID, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("add dependency: %v", err)
	}
	// bd defer <id> (no --until): status deferred, defer_until unset.
	if err := store.UpdateIssue(ctx, parent.ID, map[string]interface{}{
		"status": string(types.StatusDeferred),
	}, "tester"); err != nil {
		t.Fatalf("defer epic: %v", err)
	}

	// Default read: the deferred parent holds the child.
	work, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if ids := issueIDs(work); containsID(ids, child.ID) {
		t.Errorf("default ready = %v, want the held child absent", ids)
	}

	// The bypass lists the child again and counts it, while the parent stays
	// out: --include-deferred lifts the parent hold, not the status filter.
	work, err = store.GetReadyWork(ctx, types.WorkFilter{IncludeDeferred: true})
	if err != nil {
		t.Fatalf("GetReadyWork(IncludeDeferred): %v", err)
	}
	if ids := issueIDs(work); !containsID(ids, child.ID) {
		t.Errorf("ready with IncludeDeferred = %v, want the child listed", ids)
	} else if containsID(ids, parent.ID) {
		t.Errorf("ready with IncludeDeferred = %v, want the deferred parent still absent", ids)
	}

	defaultCount, err := store.CountReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("CountReadyWork: %v", err)
	}
	bypassCount, err := store.CountReadyWork(ctx, types.WorkFilter{IncludeDeferred: true})
	if err != nil {
		t.Fatalf("CountReadyWork(IncludeDeferred): %v", err)
	}
	if bypassCount != defaultCount+1 {
		t.Errorf("CountReadyWork = %d default, %d with IncludeDeferred; the bypass must add exactly the held child", defaultCount, bypassCount)
	}
}