//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// The residue of close_issue_checked_test.go and update_issue_checked_test.go.
//
// Those files were deleted because the conformance contracts cover the shared
// bodies they exercised — issueops.CheckVersionInTx, CheckExpectedFieldsInTx,
// UpdateIssueInTx, CloseIssueCheckedInTx — on all three legs, at equal or
// greater strength. That much held up. WHAT DID NOT is the claim that deleting
// them therefore lost nothing.
//
// EmbeddedDoltStore.UpdateIssueChecked and CloseIssueChecked (issues.go) are a
// SEPARATE COMPOSITION of those shared functions. The wrapper decides, inside
// its own withConn, whether to run the version check at all and which options
// to forward; the role path reaches the same shared bodies through
// runTransaction and never calls these methods. So a break confined to the
// wrapper — a guard that stops running, an option that stops being forwarded —
// is invisible to every contract case on every leg. After the deletion this
// package had ZERO callers of either method, and both are on the
// storage.DoltStorage interface with beads.go publishing their option types as
// caller-facing API. A review probe confirmed it: disabling the CAS branch in
// the wrapper failed the deleted test and passed all 136 contract cases.
//
// These tests are therefore deliberately NARROW. They do not re-test what the
// contracts own — no refusal taxonomy, no event counting, no rollback
// semantics. Each one asks only whether the wrapper still routes one
// precondition to the shared body that implements it, which is the one question
// the contract layer structurally cannot ask.
func TestEmbeddedCheckedWrappersRouteTheirPreconditions(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "wrapsmoke")
	ctx := t.Context()

	create := func(id string) *types.Issue {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		stored, err := te.store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		return stored
	}
	title := func(id string) string {
		iss, err := te.store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		return iss.Title
	}

	// UpdateIssueChecked forwards ExpectedVersion. A stale version must reach
	// CheckVersionInTx and refuse; the title is read back because a wrapper that
	// skipped the check would report no error AND write, and only the second
	// half distinguishes that from a check that ran and passed.
	t.Run("UpdateForwardsExpectedVersion", func(t *testing.T) {
		iss := create("wrapsmoke-ver")
		stale := iss.RowVersion - 1
		err := te.store.UpdateIssueChecked(ctx, iss.ID,
			map[string]interface{}{"title": "should not land"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: &stale})
		if !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("stale ExpectedVersion: err = %v, want ErrVersionMismatch", err)
		}
		if got := title(iss.ID); got != "wrapsmoke-ver" {
			t.Fatalf("title = %q after a refused update, want it unchanged", got)
		}
	})

	// The same routing for the field guards, which the wrapper runs
	// unconditionally rather than behind a nil check.
	t.Run("UpdateForwardsExpectedAssignee", func(t *testing.T) {
		iss := create("wrapsmoke-assignee")
		wrong := "somebody-else"
		err := te.store.UpdateIssueChecked(ctx, iss.ID,
			map[string]interface{}{"title": "should not land"}, "tester",
			storage.UpdateIssueOptions{ExpectedAssignee: &wrong})
		if err == nil {
			t.Fatal("mismatched ExpectedAssignee was accepted; the wrapper did not route the guard")
		}
		if got := title(iss.ID); got != "wrapsmoke-assignee" {
			t.Fatalf("title = %q after a refused update, want it unchanged", got)
		}
	})

	// CloseIssueChecked forwards ExpectedVersion into CloseIssueCheckedInTx.
	t.Run("CloseForwardsExpectedVersion", func(t *testing.T) {
		iss := create("wrapsmoke-close-ver")
		stale := iss.RowVersion - 1
		if _, err := te.store.CloseIssueChecked(ctx, iss.ID, "tester",
			storage.CloseIssueOptions{ExpectedVersion: &stale}); !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("stale ExpectedVersion: err = %v, want ErrVersionMismatch", err)
		}
		stored, err := te.store.GetIssue(ctx, iss.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if stored.Status != types.StatusOpen {
			t.Fatalf("status = %v after a refused close, want it still open", stored.Status)
		}
	})

	// And that the result the wrapper MAPS back is the one the shared body
	// produced: OpenChildren is the field a caller reads to explain the refusal,
	// and it is assembled in the wrapper, not returned by it.
	t.Run("CloseMapsOpenChildrenOntoTheResult", func(t *testing.T) {
		parent := create("wrapsmoke-parent")
		child := create("wrapsmoke-child")
		if err := te.store.AddDependency(ctx,
			&types.Dependency{IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		result, err := te.store.CloseIssueChecked(ctx, parent.ID, "tester", storage.CloseIssueOptions{})
		if !errors.Is(err, storage.ErrCloseOpenChildren) {
			t.Fatalf("close with an open child: err = %v, want ErrCloseOpenChildren", err)
		}
		if result.OpenChildren != 0 {
			t.Fatalf("OpenChildren = %d on the error return, want the zero result", result.OpenChildren)
		}
		forced, err := te.store.CloseIssueChecked(ctx, parent.ID, "tester", storage.CloseIssueOptions{Force: true})
		if err != nil {
			t.Fatalf("forced close: %v", err)
		}
		if forced.OpenChildren != 1 {
			t.Fatalf("forced OpenChildren = %d, want 1 mapped back from the shared body", forced.OpenChildren)
		}
	})
}
