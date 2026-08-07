package dolt

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// The residue of close_issue_checked_test.go's and update_issue_checked_test.go's
// guard suites.
//
// Those suites went because the conformance contracts cover the shared bodies
// they exercised — issueops.CheckVersionInTx, CheckExpectedFieldsInTx,
// UpdateIssueInTx, CloseIssueCheckedInTx — on all three legs, at equal or
// greater strength. What does NOT follow is that deleting them lost nothing.
//
// DoltStore.UpdateIssueChecked and CloseIssueChecked (issues.go) are a SEPARATE
// COMPOSITION of those shared functions. The wrapper decides, inside its own
// withRetryTx, which preconditions to run and which options to forward; the
// role path reaches the same shared bodies through runIssueOperationTx and
// never calls these methods. So a break confined to the wrapper is invisible to
// every contract case on every leg, and two probes measured exactly that:
// blanking the wrapper's own guard block, and dropping opts.ExpectedVersion at
// its CloseIssueCheckedInTx call, each failed the deleted tests and passed all
// 69 IssueOperations and Lifecycle contract cases at this backend. After the
// deletion this package had no caller of either method, and both are on the
// storage.DoltStorage interface with beads.go publishing their option types as
// caller-facing API.
//
// These tests are therefore deliberately NARROW. They do not re-test what the
// contracts own — no refusal taxonomy, no event counting, no rollback
// semantics, no CAS ordering. Each asks only whether the wrapper still routes
// one precondition to the shared body that implements it, on each of the three
// branches it chooses between (durable, wisp, demotion), which is the one
// question the contract layer structurally cannot ask.
func TestCheckedWrappersRouteTheirPreconditions(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	get := func(t *testing.T, id string) *types.Issue {
		t.Helper()
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		if issue == nil {
			t.Fatalf("GetIssue(%s) returned nil", id)
		}
		return issue
	}
	stale := func(t *testing.T, id string) *int64 {
		t.Helper()
		version := get(t, id).RowVersion + 1
		return &version
	}
	sptr := func(v string) *string { return &v }

	// UpdateIssueChecked forwards ExpectedVersion on its ordinary durable
	// branch. The title is read back because a wrapper that skipped the check
	// would report no error AND write, and only the second half distinguishes
	// that from a check that ran and passed.
	t.Run("UpdateForwardsExpectedVersion", func(t *testing.T) {
		createPerm(t, ctx, store, "wrapsmoke-ver")
		before := get(t, "wrapsmoke-ver").Title
		err := store.UpdateIssueChecked(ctx, "wrapsmoke-ver",
			map[string]interface{}{"title": "should not land"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: stale(t, "wrapsmoke-ver")})
		if !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("stale ExpectedVersion: err = %v, want ErrVersionMismatch", err)
		}
		if got := get(t, "wrapsmoke-ver").Title; got != before {
			t.Fatalf("title = %q after a refused update, want it unchanged", got)
		}
	})

	// The same routing for the field guards, which the wrapper runs
	// unconditionally rather than behind a nil check.
	t.Run("UpdateForwardsExpectedAssignee", func(t *testing.T) {
		createPerm(t, ctx, store, "wrapsmoke-assignee")
		before := get(t, "wrapsmoke-assignee").Title
		err := store.UpdateIssueChecked(ctx, "wrapsmoke-assignee",
			map[string]interface{}{"title": "should not land"}, "tester",
			storage.UpdateIssueOptions{ExpectedAssignee: sptr("somebody-else")})
		if !errors.Is(err, storage.ErrAssigneeMismatch) {
			t.Fatalf("mismatched ExpectedAssignee: err = %v, want ErrAssigneeMismatch", err)
		}
		if got := get(t, "wrapsmoke-assignee").Title; got != before {
			t.Fatalf("title = %q after a refused update, want it unchanged", got)
		}
	})

	// The wisp branch is a THIRD composition — updateWispChecked runs the same
	// guards over a bare BeginTx with a deferred Rollback rather than
	// withRetryTx — so it is routed to separately here.
	t.Run("UpdateRoutesAWispToTheWispChecker", func(t *testing.T) {
		createWisp(t, ctx, store, "wrapsmoke-wisp")
		before := get(t, "wrapsmoke-wisp").Title
		err := store.UpdateIssueChecked(ctx, "wrapsmoke-wisp",
			map[string]interface{}{"title": "should not land"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: stale(t, "wrapsmoke-wisp")})
		if !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("stale ExpectedVersion on a wisp: err = %v, want ErrVersionMismatch", err)
		}
		if got := get(t, "wrapsmoke-wisp").Title; got != before {
			t.Fatalf("wisp title = %q after a refused update, want it unchanged", got)
		}
	})

	// And the demotion branch, which is the reason demoteToWispInTx was
	// extracted: the guard and the migration share ONE transaction, so a
	// refused demotion leaves the row in the issues plane.
	t.Run("UpdateRunsTheGuardInsideTheDemotionTransaction", func(t *testing.T) {
		createPerm(t, ctx, store, "wrapsmoke-demote")
		err := store.UpdateIssueChecked(ctx, "wrapsmoke-demote",
			map[string]interface{}{"no_history": true, "title": "should not land"}, "tester",
			storage.UpdateIssueOptions{ExpectedVersion: stale(t, "wrapsmoke-demote")})
		if !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("stale ExpectedVersion on a demotion: err = %v, want ErrVersionMismatch", err)
		}
		if store.isActiveWisp(ctx, "wrapsmoke-demote") {
			t.Fatal("the row was demoted despite a refused precondition; the guard is outside the demotion transaction")
		}
		var inIssues int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", "wrapsmoke-demote").Scan(&inIssues); err != nil {
			t.Fatalf("count issues rows: %v", err)
		}
		if inIssues != 1 {
			t.Fatalf("issues rows = %d after a refused demotion, want the 1 the rollback preserves", inIssues)
		}
	})

	// CloseIssueChecked forwards ExpectedVersion into CloseIssueCheckedInTx.
	t.Run("CloseForwardsExpectedVersion", func(t *testing.T) {
		createPerm(t, ctx, store, "wrapsmoke-close-ver")
		if _, err := store.CloseIssueChecked(ctx, "wrapsmoke-close-ver", "tester",
			storage.CloseIssueOptions{ExpectedVersion: stale(t, "wrapsmoke-close-ver")}); !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("stale ExpectedVersion: err = %v, want ErrVersionMismatch", err)
		}
		if got := get(t, "wrapsmoke-close-ver").Status; got != types.StatusOpen {
			t.Fatalf("status = %q after a refused close, want it still open", got)
		}
	})

	// Its wisp branch, for the reason the update's has one: closeWispChecked is
	// its own transaction wrapper.
	t.Run("CloseRoutesAWispToTheWispCloser", func(t *testing.T) {
		createWisp(t, ctx, store, "wrapsmoke-close-wisp")
		if _, err := store.CloseIssueChecked(ctx, "wrapsmoke-close-wisp", "tester",
			storage.CloseIssueOptions{ExpectedVersion: stale(t, "wrapsmoke-close-wisp")}); !errors.Is(err, storage.ErrVersionMismatch) {
			t.Fatalf("stale ExpectedVersion on a wisp: err = %v, want ErrVersionMismatch", err)
		}
		if got := get(t, "wrapsmoke-close-wisp").Status; got != types.StatusOpen {
			t.Fatalf("wisp status = %q after a refused close, want it still open", got)
		}
	})

	// And that the result the wrapper MAPS back is the one the shared body
	// produced: OpenChildren is the field a caller reads to explain the
	// refusal, and it is assembled in the wrapper, not returned by it.
	t.Run("CloseMapsOpenChildrenOntoTheResult", func(t *testing.T) {
		createPerm(t, ctx, store, "wrapsmoke-parent")
		createPerm(t, ctx, store, "wrapsmoke-child")
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: "wrapsmoke-child", DependsOnID: "wrapsmoke-parent", Type: types.DepParentChild,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		result, err := store.CloseIssueChecked(ctx, "wrapsmoke-parent", "tester", storage.CloseIssueOptions{})
		if !errors.Is(err, storage.ErrCloseOpenChildren) {
			t.Fatalf("close with an open child: err = %v, want ErrCloseOpenChildren", err)
		}
		if result.OpenChildren != 0 {
			t.Fatalf("OpenChildren = %d on the error return, want the zero result", result.OpenChildren)
		}
		forced, err := store.CloseIssueChecked(ctx, "wrapsmoke-parent", "tester", storage.CloseIssueOptions{Force: true})
		if err != nil {
			t.Fatalf("forced close: %v", err)
		}
		if forced.Unchanged || forced.OpenChildren != 1 {
			t.Fatalf("forced close = %+v, want a real close reporting the 1 child the shared body counted", forced)
		}
	})

	// An id in neither plane reaches neither branch's shared body, so the
	// not-found answer is the wrapper's own.
	t.Run("CloseOfAnAbsentIDIsNotFound", func(t *testing.T) {
		if _, err := store.CloseIssueChecked(ctx, "wrapsmoke-absent", "tester",
			storage.CloseIssueOptions{}); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("close of an absent id: err = %v, want ErrNotFound", err)
		}
	})

	// THE SIX BELOW ARE THE REST OF THE DECISION SURFACE, and they exist
	// because the eight above were not enough.
	//
	// The cases above were written from the two mutants that had already been
	// measured, which pinned the version guard on three branches and left
	// everything else the wrappers decide unobserved. A review then broke six
	// more single statements — one option not forwarded, one guard pair
	// skipped, two writes never issued, one flag dropped, one result field
	// constant — and every remaining test in this package plus every contract
	// case stayed green on all of them.
	//
	// The lesson is the reason these are grouped rather than merged upward: a
	// residue written from the mutants you happened to run pins those mutants.
	// A residue has to be written from the wrapper's OWN branches and options,
	// asking of each one whether anything would notice it going away. Half of
	// what follows is a POSITIVE assertion for a branch whose refusal was
	// already covered — a wrapper that refuses correctly and never writes
	// passed everything before this.

	// W-A. ExpectedStatus is the third option of UpdateIssueOptions and the
	// only one no case above forwards. It is the claim-on-behalf guard: a
	// guarded write that should refuse with ErrStatusMismatch silently applied.
	t.Run("UpdateForwardsExpectedStatus", func(t *testing.T) {
		createPerm(t, ctx, store, "wrapsmoke-status")
		before := get(t, "wrapsmoke-status").Title
		err := store.UpdateIssueChecked(ctx, "wrapsmoke-status",
			map[string]interface{}{"title": "should not land"}, "tester",
			storage.UpdateIssueOptions{ExpectedStatus: sptr(string(types.StatusClosed))})
		if err == nil {
			t.Fatal("mismatched ExpectedStatus was accepted; the wrapper does not forward it")
		}
		if got := get(t, "wrapsmoke-status").Title; got != before {
			t.Fatalf("title = %q after a refused update, want it unchanged", got)
		}
	})

	// W-B. The wisp branch runs its own guard pair, and the wisp case above
	// exercises only the version check. BOTH options are asserted separately:
	// the branch passes them to one call, so a test that sent only one would
	// leave the other free to be dropped.
	t.Run("UpdateForwardsFieldGuardsOnTheWispBranch", func(t *testing.T) {
		for name, opts := range map[string]storage.UpdateIssueOptions{
			"assignee": {ExpectedAssignee: sptr("somebody-else")},
			"status":   {ExpectedStatus: sptr(string(types.StatusClosed))},
		} {
			t.Run(name, func(t *testing.T) {
				id := "wrapsmoke-wisp-guard-" + name
				createWisp(t, ctx, store, id)
				before := get(t, id).Title
				err := store.UpdateIssueChecked(ctx, id,
					map[string]interface{}{"title": "should not land"}, "tester", opts)
				if err == nil {
					t.Fatalf("mismatched Expected%s on a wisp was accepted; the wisp branch drops that guard", name)
				}
				if got := get(t, id).Title; got != before {
					t.Fatalf("wisp title = %q after a refused update, want it unchanged", got)
				}
			})
		}
	})

	// W-F. The positive half of that branch: guards passing must WRITE.
	t.Run("UpdateAppliesTheWriteOnTheWispBranch", func(t *testing.T) {
		createWisp(t, ctx, store, "wrapsmoke-wisp-write")
		if err := store.UpdateIssueChecked(ctx, "wrapsmoke-wisp-write",
			map[string]interface{}{"title": "landed"}, "tester",
			storage.UpdateIssueOptions{}); err != nil {
			t.Fatalf("unguarded wisp update: %v", err)
		}
		if got := get(t, "wrapsmoke-wisp-write").Title; got != "landed" {
			t.Fatalf("wisp title = %q, want the update the wrapper reported committing", got)
		}
	})

	// W-C. And the positive half of the demotion branch: the guard passing
	// must migrate the row, not merely decline to refuse.
	t.Run("UpdateAppliesTheDemotion", func(t *testing.T) {
		createPerm(t, ctx, store, "wrapsmoke-demote-ok")
		if err := store.UpdateIssueChecked(ctx, "wrapsmoke-demote-ok",
			map[string]interface{}{"no_history": true}, "tester",
			storage.UpdateIssueOptions{}); err != nil {
			t.Fatalf("unguarded demotion: %v", err)
		}
		if !store.isActiveWisp(ctx, "wrapsmoke-demote-ok") {
			t.Fatal("the row was not demoted; the wrapper reported success without migrating it")
		}
	})

	// W-D. Force reaches the wisp close branch. Without it a blocked wisp is
	// permanently uncloseable through this wrapper.
	t.Run("CloseForwardsForceOnTheWispBranch", func(t *testing.T) {
		createWisp(t, ctx, store, "wrapsmoke-wisp-blocked")
		createPerm(t, ctx, store, "wrapsmoke-wisp-blocker")
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: "wrapsmoke-wisp-blocked", DependsOnID: "wrapsmoke-wisp-blocker", Type: types.DepBlocks,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		if _, err := store.CloseIssueChecked(ctx, "wrapsmoke-wisp-blocked", "tester",
			storage.CloseIssueOptions{Force: true}); err != nil {
			t.Fatalf("forced close of a blocked wisp: %v — the wisp branch does not forward Force", err)
		}
		if got := get(t, "wrapsmoke-wisp-blocked").Status; got != types.StatusClosed {
			t.Fatalf("wisp status = %q after a forced close, want it closed", got)
		}
	})

	// W-E. Unchanged is assembled by the wrapper from the shared body's
	// AlreadyClosed. Every case above asserts it only on the false side, so a
	// constant false passed them all — and a caller keying "did I actually
	// close this" could not tell a re-close from a close.
	t.Run("CloseReportsUnchangedOnAnIdempotentReClose", func(t *testing.T) {
		createPerm(t, ctx, store, "wrapsmoke-reclose")
		first, err := store.CloseIssueChecked(ctx, "wrapsmoke-reclose", "tester", storage.CloseIssueOptions{})
		if err != nil || first.Unchanged {
			t.Fatalf("first close = %+v, %v; want a real close", first, err)
		}
		second, err := store.CloseIssueChecked(ctx, "wrapsmoke-reclose", "tester", storage.CloseIssueOptions{})
		if err != nil {
			t.Fatalf("re-close: %v", err)
		}
		if !second.Unchanged {
			t.Fatal("re-close reported Unchanged = false; the wrapper does not map AlreadyClosed back")
		}
	})

	// W-G. Reason and Session are the close wrapper's two ATTRIBUTION options,
	// and nothing in the tree read them back through this seam — a wrapper that
	// dropped both recorded an anonymous close and every test stayed green.
	// They are asserted together because they travel in one call, and on the
	// durable and wisp branches separately because each branch spells that call
	// itself.
	t.Run("CloseForwardsReasonAndSession", func(t *testing.T) {
		for name, create := range map[string]func(*testing.T, context.Context, *DoltStore, string){
			"durable": createPerm,
			"wisp":    createWisp,
		} {
			t.Run(name, func(t *testing.T) {
				id := "wrapsmoke-attrib-" + name
				create(t, ctx, store, id)
				if _, err := store.CloseIssueChecked(ctx, id, "tester", storage.CloseIssueOptions{
					Reason: "because-" + name, Session: "session-" + name,
				}); err != nil {
					t.Fatalf("close: %v", err)
				}
				closed := get(t, id)
				if closed.CloseReason != "because-"+name {
					t.Errorf("CloseReason = %q, want the reason the wrapper was given", closed.CloseReason)
				}
				if closed.ClosedBySession != "session-"+name {
					t.Errorf("ClosedBySession = %q, want the session the wrapper was given", closed.ClosedBySession)
				}
			})
		}
	})
}
