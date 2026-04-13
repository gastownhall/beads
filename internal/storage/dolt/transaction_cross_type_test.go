package dolt

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestAddDependency_CrossTypeBondRejection verifies that AddDependency rejects
// dependencies whose two sides are different kinds (one wisp, one non-wisp
// issue), returning a typed storage.ErrCrossTypeBond.
//
// Background: AddDependency routes its INSERT to dependencies vs
// wisp_dependencies based on the left-side type only (isActiveWisp(IssueID)).
// Without the cross-type check, a wisp↔issue bond either trips fk_dep_issue
// at INSERT time with an opaque Dolt 1452, or silently inserts an
// orphan-pointing row into wisp_dependencies that breaks downstream joins.
// The typed-error contract lets callers recover deliberately instead of
// pattern-matching on Dolt's FK error string.
func TestAddDependency_CrossTypeBondRejection(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create two distinct issues (so the issue→issue happy path is a
	// real INSERT, not an idempotency short-circuit).
	issueA := &types.Issue{
		Title:     "issue side A",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issueA, "tester"); err != nil {
		t.Fatalf("CreateIssue (issueA) failed: %v", err)
	}
	issueB := &types.Issue{
		Title:     "issue side B",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issueB, "tester"); err != nil {
		t.Fatalf("CreateIssue (issueB) failed: %v", err)
	}

	// Create two distinct wisps (same reasoning).
	wispA := &types.Issue{
		Title:     "wisp side A",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wispA, "tester"); err != nil {
		t.Fatalf("CreateIssue (wispA) failed: %v", err)
	}
	wispB := &types.Issue{
		Title:     "wisp side B",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wispB, "tester"); err != nil {
		t.Fatalf("CreateIssue (wispB) failed: %v", err)
	}

	// Sanity-check the routing precondition: wisps must be active
	// (in the wisps table), issues must NOT be.
	if !store.isActiveWisp(ctx, wispA.ID) {
		t.Fatalf("precondition: wispA expected to be active wisp, isActiveWisp=false (id=%s)", wispA.ID)
	}
	if !store.isActiveWisp(ctx, wispB.ID) {
		t.Fatalf("precondition: wispB expected to be active wisp, isActiveWisp=false (id=%s)", wispB.ID)
	}
	if store.isActiveWisp(ctx, issueA.ID) {
		t.Fatalf("precondition: issueA must not be an active wisp (id=%s)", issueA.ID)
	}
	if store.isActiveWisp(ctx, issueB.ID) {
		t.Fatalf("precondition: issueB must not be an active wisp (id=%s)", issueB.ID)
	}

	cases := []struct {
		name        string
		left, right string
		wantErr     error // nil = success expected
	}{
		{"issue->issue", issueA.ID, issueB.ID, nil},
		{"wisp->wisp", wispA.ID, wispB.ID, nil},
		{"wisp->issue", wispA.ID, issueA.ID, storage.ErrCrossTypeBond},
		{"issue->wisp", issueA.ID, wispA.ID, storage.ErrCrossTypeBond},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := store.RunInTransaction(ctx, "test: cross-type bond", func(tx storage.Transaction) error {
				return tx.AddDependency(ctx, &types.Dependency{
					IssueID:     tc.left,
					DependsOnID: tc.right,
					Type:        types.DepRelated,
				}, "tester")
			})

			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected %v, got nil", tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected errors.Is(err, %v) to be true, got %v", tc.wantErr, err)
			}
		})
	}
}
