package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestIsBlockedBatch_ReadsDenormalizedColumnInOneCall proves the batched
// readiness read an orchestrator projects from: blocked and unblocked ids come
// back in one map, ids without a row are absent, and the result agrees with
// the per-issue IsBlocked answer.
func TestIsBlockedBatch_ReadsDenormalizedColumnInOneCall(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	blocker := &types.Issue{ID: "batch-blocker", Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	blocked := &types.Issue{ID: "batch-blocked", Title: "Blocked", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	free := &types.Issue{ID: "batch-free", Title: "Free", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	for _, issue := range []*types.Issue{blocker, blocked, free} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: blocked.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	got, err := store.IsBlockedBatch(ctx, []string{blocked.ID, free.ID, blocker.ID, "batch-missing"})
	if err != nil {
		t.Fatalf("IsBlockedBatch failed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("IsBlockedBatch returned %d rows, want 3 (missing id must be absent): %v", len(got), got)
	}
	if _, present := got["batch-missing"]; present {
		t.Fatalf("IsBlockedBatch returned a row for an unknown id: %v", got)
	}
	for id, want := range map[string]bool{blocked.ID: true, free.ID: false, blocker.ID: false} {
		if got[id] != want {
			t.Errorf("IsBlockedBatch[%s] = %v, want %v", id, got[id], want)
		}
		single, _, err := store.IsBlocked(ctx, id)
		if err != nil {
			t.Fatalf("IsBlocked(%s) failed: %v", id, err)
		}
		if single != got[id] {
			t.Errorf("IsBlockedBatch[%s] = %v disagrees with IsBlocked = %v", id, got[id], single)
		}
	}

	// Closing the blocker flips the denormalized column; the batch must see it.
	if err := store.CloseIssue(ctx, blocker.ID, "done", "tester", ""); err != nil {
		t.Fatalf("failed to close blocker: %v", err)
	}
	got, err = store.IsBlockedBatch(ctx, []string{blocked.ID})
	if err != nil {
		t.Fatalf("IsBlockedBatch after close failed: %v", err)
	}
	if got[blocked.ID] {
		t.Fatalf("IsBlockedBatch[%s] still true after its only blocker closed", blocked.ID)
	}
}

func TestIsBlockedBatch_EmptyInputIsEmptyResult(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	got, err := store.IsBlockedBatch(ctx, nil)
	if err != nil {
		t.Fatalf("IsBlockedBatch(nil) failed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("IsBlockedBatch(nil) = %v, want empty", got)
	}
}
