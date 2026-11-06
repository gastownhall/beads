//go:build cgo

package embeddeddolt_test

import (
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// plainIDs extracts the IDs of a plain issue page.
func plainIDs(items []*types.Issue) []string {
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

// seedUndatedDeferredPlane seeds GH#7300's fixture: an epic deferred with no
// --until (status deferred, no defer_until), an open durable child hanging off
// it by a parent-child edge, an optional open wisp (ephemeral) child of the
// same kind, and one unrelated free issue as the positive control. The parent
// starts open and is deferred with a bare status write, which is the write
// both `bd defer` without --until and `bd update --status deferred` make.
func seedUndatedDeferredPlane(t *testing.T, te *testEnv, prefix string, withWispChild bool) {
	t.Helper()
	ctx := t.Context()
	create := func(iss *types.Issue) {
		t.Helper()
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}
	dep := func(child string) *types.Dependency {
		return &types.Dependency{IssueID: child, DependsOnID: prefix + "-parent", Type: types.DepParentChild}
	}

	parent := &types.Issue{ID: prefix + "-parent", Title: "undated deferred epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
	create(parent)
	for _, iss := range []*types.Issue{
		{ID: prefix + "-child", Title: "child of undated deferred epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: prefix + "-free", Title: "unrelated free issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		create(iss)
	}
	if err := te.store.AddDependency(ctx, dep(prefix+"-child"), "tester"); err != nil {
		t.Fatalf("AddDependency %s-child -> parent: %v", prefix, err)
	}
	if withWispChild {
		create(&types.Issue{ID: prefix + "-wchild", Title: "wisp child of undated deferred epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true})
		if err := te.store.AddDependency(ctx, dep(prefix+"-wchild"), "tester"); err != nil {
			t.Fatalf("AddDependency %s-wchild -> parent: %v", prefix, err)
		}
	}

	// The bare status write that both undated-defer writers make.
	if err := te.store.UpdateIssue(ctx, prefix+"-parent", map[string]interface{}{
		"status": string(types.StatusDeferred),
	}, "tester"); err != nil {
		t.Fatalf("defer %s-parent: %v", prefix, err)
	}
	// defer_until must remain NULL: that is what separates the indefinite
	// icebox from a snooze.
	var deferUntil *time.Time
	te.queryScalar(t, ctx, "SELECT defer_until FROM issues WHERE id = ?", []any{prefix + "-parent"}, &deferUntil)
	if deferUntil != nil {
		t.Fatalf("defer_until = %v, want NULL (the icebox is undated)", deferUntil)
	}
}

// TestReadyWorkHidesChildrenOfUndatedDeferredParent pins GH#7300: a
// parent put on ice with status 'deferred' and no defer_until hides its
// direct children from ready work, exactly as a parent with a future
// defer_until does. No row in any fixture database of this test holds a
// future defer_until, so only the status can trip the deferred-parent gates.
func TestReadyWorkHidesChildrenOfUndatedDeferredParent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	// The classic store (embedded dolt delegates its ready reads to the
	// shared issueops code) returns the free issue and withholds both the
	// deferred epic and its child.
	t.Run("ClassicStore", func(t *testing.T) {
		te := newTestEnv(t, "udc")
		seedUndatedDeferredPlane(t, te, "udc", false)

		got, err := te.store.GetReadyWork(t.Context(), types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		if want := []string{"udc-free"}; !slices.Equal(sortedIDs(plainIDs(got)), want) {
			t.Errorf("GetReadyWork = %v, want %v (the undated deferred epic's child must be withheld)", plainIDs(got), want)
		}
	})
}

// TestReadyWorkUndatedDeferredPredicateVariants pins predicate boundaries the
// narrow fix must preserve: a dependency type other than parent-child never
// hides anything, and an undated deferred parent hides its wisp children too.
func TestReadyWorkUndatedDeferredPredicateVariants(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("RelatedEdgesStayReady", func(t *testing.T) {
		te := newTestEnv(t, "udl")
		seedUndatedDeferredPlane(t, te, "udl", false)
		ctx := t.Context()
		rel := &types.Issue{ID: "udl-rel", Title: "related to the deferred epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, rel, "tester"); err != nil {
			t.Fatalf("CreateIssue udl-rel: %v", err)
		}
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "udl-rel", DependsOnID: "udl-parent", Type: types.DepRelated}, "tester"); err != nil {
			t.Fatalf("AddDependency udl-rel -> udl-parent: %v", err)
		}

		got, err := te.store.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		ids := sortedIDs(plainIDs(got))
		want := []string{"udl-free", "udl-rel"}
		if !slices.Equal(ids, want) {
			t.Errorf("GetReadyWork = %v, want %v (a related edge is not a parent-child edge; only the child hides)", ids, want)
		}
	})

	// An expired dated snooze keeps its children ready before any wake: the
	// date leg has gone false and the NULL guard keeps the status leg away
	// from a row that still reads 'deferred'. The wake sweep is advisory
	// (wake_defers.go), so a read before it must not hide the child — the
	// narrow predicate exists to leave this row alone.
	t.Run("ExpiredDatedDeferLeavesChildReadyBeforeWake", func(t *testing.T) {
		te := newTestEnv(t, "udx")
		ctx := t.Context()
		for _, iss := range []*types.Issue{
			{ID: "udx-parent", Title: "snoozed epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic},
			{ID: "udx-child", Title: "child of snoozed epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "udx-free", Title: "unrelated free issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		} {
			if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", iss.ID, err)
			}
		}
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "udx-child", DependsOnID: "udx-parent", Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("AddDependency udx-child -> udx-parent: %v", err)
		}
		past := time.Now().UTC().Add(-48 * time.Hour)
		// Written with a raw UPDATE, not UpdateIssue: the row must hold BOTH
		// status 'deferred' and the expired date at read time, which is the
		// state a snooze whose date passed leaves behind until the (advisory)
		// wake sweep opens it. The write path would normalize around it.
		te.exec(t, ctx, "UPDATE issues SET status = 'deferred', defer_until = ? WHERE id = ?", past, "udx-parent")
		var status string
		var deferUntil *time.Time
		te.queryScalar(t, ctx, "SELECT status, defer_until FROM issues WHERE id = ?", []any{"udx-parent"}, &status, &deferUntil)
		if status != string(types.StatusDeferred) || deferUntil == nil || !deferUntil.Before(time.Now().UTC()) {
			t.Fatalf("udx-parent = status %q defer_until %v, want deferred with a past date", status, deferUntil)
		}

		// The classic store's GetReadyWork wakes expired snoozes before it
		// reads (queries.go wakeExpiredDefers), so the pre-wake contract is
		// pinned through a direct issueops read transaction, where no wake
		// runs: the date leg has gone false, and the NULL guard keeps the
		// status leg away from this still-'deferred' row, so the child stays
		// eligible exactly as before this fix existed.
		var preWakeIDs []string
		withCountingReadTx(t, te, func(tx issueops.DBTX) error {
			ready, rErr := issueops.GetReadyWorkInTx(ctx, tx, types.WorkFilter{})
			if rErr != nil {
				return rErr
			}
			preWakeIDs = plainIDs(ready)
			return nil
		})
		if got, want := sortedIDs(preWakeIDs), []string{"udx-child", "udx-free"}; !slices.Equal(got, want) {
			t.Errorf("pre-wake read = %v, want %v (an expired snooze must not hide its child; only the wake opens it)", got, want)
		}

		// Through the store the advisory wake has already opened the parent,
		// so parent and child are both ready — the wrapper contract.
		woke, err := te.store.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		if got, want := sortedIDs(plainIDs(woke)), []string{"udx-child", "udx-free", "udx-parent"}; !slices.Equal(got, want) {
			t.Errorf("post-wake read = %v, want %v (the wake opens the expired snooze)", got, want)
		}

		// The dated leg keeps matching a deferred-status row while its date
		// is still in the future: that is `bd defer --until`, and this fix
		// must not loosen it. Same raw-UPDATE seeding for the same reason.
		future := time.Now().UTC().Add(48 * time.Hour)
		te.exec(t, ctx, "UPDATE issues SET defer_until = ? WHERE id = ?", future, "udx-parent")
		snoozed, err := te.store.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork (snoozed): %v", err)
		}
		if ids := sortedIDs(plainIDs(snoozed)); !slices.Equal(ids, []string{"udx-free"}) {
			t.Errorf("GetReadyWork after re-snoozing = %v, want [udx-free] (a future defer_until hides the child whatever the status)", ids)
		}
	})

	// The two legs need both sides: an open parent with no date touches
	// neither, so its direct children stay ready.
	t.Run("OpenUndatedParentLeavesChildReady", func(t *testing.T) {
		te := newTestEnv(t, "udo")
		ctx := t.Context()
		for _, iss := range []*types.Issue{
			{ID: "udo-parent", Title: "open epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic},
			{ID: "udo-child", Title: "child of open epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "udo-free", Title: "unrelated free issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		} {
			if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", iss.ID, err)
			}
		}
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "udo-child", DependsOnID: "udo-parent", Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("AddDependency udo-child -> udo-parent: %v", err)
		}

		got, err := te.store.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		ids := sortedIDs(plainIDs(got))
		want := []string{"udo-child", "udo-free", "udo-parent"}
		if !slices.Equal(ids, want) {
			t.Errorf("GetReadyWork = %v, want %v (an open parent hides nothing; the open parent is itself ready)", ids, want)
		}
	})

	// The front row a claim can win must not be a child on ice. The child
	// outranks the free issue so at base the claim would deterministically
	// pick the leaked child; after the fix the free issue is the front row.
	t.Run("ClaimSkipsChildOfUndatedDeferredParent", func(t *testing.T) {
		te := newTestEnv(t, "udk")
		ctx := t.Context()
		for _, iss := range []*types.Issue{
			{ID: "udk-parent", Title: "undated deferred epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic},
			{ID: "udk-child", Title: "child on ice", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
			{ID: "udk-free", Title: "unrelated free issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		} {
			if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", iss.ID, err)
			}
		}
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "udk-child", DependsOnID: "udk-parent", Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("AddDependency udk-child -> udk-parent: %v", err)
		}
		if err := te.store.UpdateIssue(ctx, "udk-parent", map[string]interface{}{
			"status": string(types.StatusDeferred),
		}, "tester"); err != nil {
			t.Fatalf("defer udk-parent: %v", err)
		}

		claimed, err := te.store.ClaimReadyIssue(ctx, types.WorkFilter{}, "worker")
		if err != nil {
			t.Fatalf("ClaimReadyIssue: %v", err)
		}
		if claimed == nil || claimed.ID != "udk-free" {
			t.Errorf("claimed = %v, want udk-free (a claim must not pick up a child held by an iceboxed parent)", claimed)
		}
	})

	t.Run("UndatedWispParentHidesWispChild", func(t *testing.T) {
		te := newTestEnv(t, "udw")
		ctx := t.Context()
		create := func(id string) *types.Issue {
			iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
			if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", id, err)
			}
			return iss
		}
		create("udw-parent")
		create("udw-child")
		create("udw-free")
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "udw-child", DependsOnID: "udw-parent", Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("AddDependency: %v", err)
		}
		if err := te.store.UpdateIssue(ctx, "udw-parent", map[string]interface{}{
			"status": string(types.StatusDeferred),
		}, "tester"); err != nil {
			t.Fatalf("defer udw-parent: %v", err)
		}

		got, err := te.store.GetReadyWork(ctx, types.WorkFilter{IncludeEphemeral: true})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		ids := sortedIDs(plainIDs(got))
		if want := []string{"udw-free"}; !slices.Equal(ids, want) {
			t.Errorf("GetReadyWork = %v, want %v (the wisp child of an undated deferred wisp parent must be withheld)", ids, want)
		}
	})
}
