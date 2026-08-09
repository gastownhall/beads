package dolt

import (
	"errors"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// The single-threaded half of this file is gone.
// RunLifecycleUpdateAssigneeTransferFence and
// RunLifecycleUpdateConditionalGuardsGateOrdinaryEdits state the same
// guard promises at three backends and are strictly stronger on the one this
// file claimed and could not pin: whether `--if-assignee ''` is a real
// "expected unassigned" guard rather than an absent one. What the wrapper
// COMPOSITION still owes is in checked_wrapper_smoke_test.go.
//
// The race stays. It is the structural promise no contract case can hold
// (backend/conformance/memories_contract.go:51-57 declares the class), and it
// is the only test in the tree that drives two guarded reassigns of one row
// through withRetryTx's commit-time collision replay.

// TestGuardedReassignConcurrent races two guarded reassigns of the same row —
// both conditioned on the same expected assignee — and requires exactly one
// winner: the loser must observe the winner's write (via the shared-tx guard
// re-check after withRetryTx replays the commit-time row_lock collision) and
// refuse with ErrAssigneeMismatch, never silently clobber. This is the CAS
// property the blind `bd update -a` check-then-act pattern lacks.
func TestGuardedReassignConcurrent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "ug-race")
	if err := store.ClaimIssue(ctx, "ug-race", "worker"); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	expected := "worker"
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, newAssignee := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func(i int, newAssignee string) {
			defer wg.Done()
			errs[i] = store.UpdateIssueChecked(ctx, "ug-race",
				map[string]interface{}{"assignee": newAssignee}, newAssignee,
				storage.UpdateIssueOptions{ExpectedAssignee: &expected})
		}(i, newAssignee)
	}
	wg.Wait()

	winners := 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, storage.ErrAssigneeMismatch):
			// the loser's verdict
		default:
			t.Fatalf("racer %d unexpected error: %v", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1 (errs: %v)", winners, errs)
	}

	iss, err := store.GetIssue(ctx, "ug-race")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if iss.Assignee != "alpha" && iss.Assignee != "beta" {
		t.Fatalf("final assignee = %q, want the winner's value", iss.Assignee)
	}
}
