//go:build cgo

package embeddeddolt_test

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// undatedDeferredFixture creates issues and parent-child edges the way the CLI
// does, then defers each named parent the way plain `bd defer` does: status
// deferred, no defer_until.
type undatedDeferredFixture struct {
	t  *testing.T
	te *testEnv
}

func (f undatedDeferredFixture) create(id string, ephemeral bool) {
	f.t.Helper()
	iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, IssueType: types.TypeTask, Ephemeral: ephemeral}
	if err := f.te.store.CreateIssue(f.t.Context(), iss, "tester"); err != nil {
		f.t.Fatalf("CreateIssue %s: %v", id, err)
	}
}

func (f undatedDeferredFixture) child(childID, parentID string) {
	f.t.Helper()
	dep := &types.Dependency{IssueID: childID, DependsOnID: parentID, Type: types.DepParentChild}
	if err := f.te.store.AddDependency(f.t.Context(), dep, "tester"); err != nil {
		f.t.Fatalf("AddDependency %s -> %s: %v", childID, parentID, err)
	}
}

func (f undatedDeferredFixture) setStatus(id string, status types.Status) {
	f.t.Helper()
	if err := f.te.store.UpdateIssue(f.t.Context(), id, map[string]interface{}{"status": string(status)}, "tester"); err != nil {
		f.t.Fatalf("UpdateIssue %s status=%s: %v", id, status, err)
	}
}

func (f undatedDeferredFixture) assertUndated(id string) {
	f.t.Helper()
	iss, err := f.te.store.GetIssue(f.t.Context(), id)
	if err != nil {
		f.t.Fatalf("GetIssue %s: %v", id, err)
	}
	if iss.DeferUntil != nil {
		f.t.Fatalf("%s carries defer_until %v; the fixture needs an undated deferral", id, iss.DeferUntil)
	}
}

func (f undatedDeferredFixture) ready(filter types.WorkFilter) []string {
	f.t.Helper()
	items, err := f.te.store.GetReadyWork(f.t.Context(), filter)
	if err != nil {
		f.t.Fatalf("GetReadyWork(%+v): %v", filter, err)
	}
	ids := make([]string, 0, len(items))
	for _, iss := range items {
		ids = append(ids, iss.ID)
	}
	return sortedIDs(ids)
}

func (f undatedDeferredFixture) wantReady(filter types.WorkFilter, want ...string) {
	f.t.Helper()
	got := f.ready(filter)
	if strings.Join(got, ",") != strings.Join(sortedIDs(want), ",") {
		f.t.Errorf("ready(%+v) = %v, want %v", filter, got, sortedIDs(want))
	}
}

// TestReadyHidesChildrenOfUndatedDeferredParentAcrossTables covers every
// parent-table x dependency-table pair the child scan reads: a durable and an
// ephemeral parent, each deferred without a date, with one durable and one
// ephemeral child apiece. Each child is hidden whichever table holds its parent
// or its edge. A grandchild is not hidden: only direct children are.
func TestReadyHidesChildrenOfUndatedDeferredParentAcrossTables(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	f := undatedDeferredFixture{t: t, te: newTestEnv(t, "ux")}

	f.create("ux-p", false)
	f.create("ux-wp", true)
	f.create("ux-c-ii", false) // dependencies -> issues
	f.create("ux-c-iw", false) // dependencies -> wisps
	f.create("ux-w-wi", true)  // wisp_dependencies -> issues
	f.create("ux-w-ww", true)  // wisp_dependencies -> wisps
	f.create("ux-grand", false)
	f.create("ux-free", false)
	f.create("ux-wfree", true)
	f.child("ux-c-ii", "ux-p")
	f.child("ux-c-iw", "ux-wp")
	f.child("ux-w-wi", "ux-p")
	f.child("ux-w-ww", "ux-wp")
	f.child("ux-grand", "ux-c-ii")
	f.setStatus("ux-p", types.StatusDeferred)
	f.setStatus("ux-wp", types.StatusDeferred)
	f.assertUndated("ux-p")
	f.assertUndated("ux-wp")

	f.wantReady(types.WorkFilter{IncludeEphemeral: true}, "ux-free", "ux-wfree", "ux-grand")
	f.wantReady(types.WorkFilter{}, "ux-free", "ux-grand")
}

// TestReadyIncludeDeferredShowsChildrenOfUndatedDeferredParent: IncludeDeferred
// lifts the child exclusion, while the parent itself stays out under the
// normal ready status filter (deferred is not open or in_progress).
func TestReadyIncludeDeferredShowsChildrenOfUndatedDeferredParent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	f := undatedDeferredFixture{t: t, te: newTestEnv(t, "ui")}

	f.create("ui-p", false)
	f.create("ui-c", false)
	f.create("ui-free", false)
	f.child("ui-c", "ui-p")
	f.setStatus("ui-p", types.StatusDeferred)
	f.assertUndated("ui-p")

	f.wantReady(types.WorkFilter{}, "ui-free")
	f.wantReady(types.WorkFilter{IncludeDeferred: true}, "ui-c", "ui-free")
	parent := "ui-p"
	f.wantReady(types.WorkFilter{IncludeDeferred: true, ParentID: &parent}, "ui-c")
}

// TestReadyShowsChildrenAfterUndatedDeferredParentIsUndeferred: moving the
// parent back to open, still with no date, returns its child to ready work.
func TestReadyShowsChildrenAfterUndatedDeferredParentIsUndeferred(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	f := undatedDeferredFixture{t: t, te: newTestEnv(t, "uu")}

	f.create("uu-p", false)
	f.create("uu-c", false)
	f.create("uu-free", false)
	f.child("uu-c", "uu-p")
	f.setStatus("uu-p", types.StatusDeferred)
	f.assertUndated("uu-p")
	f.wantReady(types.WorkFilter{}, "uu-free")

	f.setStatus("uu-p", types.StatusOpen)
	f.assertUndated("uu-p")
	f.wantReady(types.WorkFilter{}, "uu-c", "uu-free", "uu-p")
}
