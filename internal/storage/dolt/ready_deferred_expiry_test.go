package dolt

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestGetReadyWork_ExpiredDatedDeferredParentHoldsChildUntilWake pins the
// expiry half of the deferred-parent hold (GH#7300): a persisted
// status='deferred' parent holds its direct child even once its defer_until
// has passed, until a lazy wake reopens it.
//
// The wake sweep runs OUTSIDE the read transaction and is advisory by
// contract (wakeExpiredDefers swallows every failure, and deferWakeSweepEligible
// skips it on a strict read-only store), so a read whose sweep was skipped or
// failed sees exactly the un-woken state read here directly through
// issueops.GetReadyWorkInTx: the child stays held. A writable store's sweep
// reopens the parent, and the next read returns parent and child alike.
func TestGetReadyWork_ExpiredDatedDeferredParentHoldsChildUntilWake(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	parent := &types.Issue{ID: "rw-wake-epic", Title: "Expired dated epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
	child := &types.Issue{ID: "rw-wake-child", Title: "Child of expired dated epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
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
	// bd defer <id> --until <past>: status deferred with an expired date.
	if err := store.UpdateIssue(ctx, parent.ID, map[string]interface{}{
		"status":      string(types.StatusDeferred),
		"defer_until": time.Now().UTC().Add(-time.Hour),
	}, "tester"); err != nil {
		t.Fatalf("defer epic: %v", err)
	}

	// The un-woken read: what a client whose sweep was skipped (strict
	// --readonly) or failed (swallowed by contract) sees.
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin read tx: %v", err)
	}
	unwoken, err := issueops.GetReadyWorkInTx(ctx, tx, types.WorkFilter{})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("GetReadyWorkInTx (un-woken): %v", err)
	}
	ids := issueIDs(unwoken)
	if containsID(ids, child.ID) {
		t.Errorf("un-woken ready = %v, want the child of the expired dated deferred parent held", ids)
	}
	if containsID(ids, parent.ID) {
		t.Errorf("un-woken ready = %v, want the deferred parent absent", ids)
	}
	// The bypass still lifts the hold; the parent stays out by status.
	unwokenBypass, err := issueops.GetReadyWorkInTx(ctx, tx, types.WorkFilter{IncludeDeferred: true})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("GetReadyWorkInTx (un-woken, IncludeDeferred): %v", err)
	}
	ids = issueIDs(unwokenBypass)
	if !containsID(ids, child.ID) {
		t.Errorf("un-woken ready with IncludeDeferred = %v, want the child listed", ids)
	}
	if containsID(ids, parent.ID) {
		t.Errorf("un-woken ready with IncludeDeferred = %v, want the deferred parent absent", ids)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback read tx: %v", err)
	}

	// The writable store's lazy wake reopens the parent, and both parent and
	// child are ready again.
	woken, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork (after wake): %v", err)
	}
	ids = issueIDs(woken)
	if !containsID(ids, child.ID) || !containsID(ids, parent.ID) {
		t.Errorf("ready after the wake = %v, want both the reopened parent and its child", ids)
	}
	reopened, err := store.GetIssue(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetIssue %s: %v", parent.ID, err)
	}
	if reopened.Status != types.StatusOpen {
		t.Errorf("parent status after the wake = %q, want %q", reopened.Status, types.StatusOpen)
	}
}