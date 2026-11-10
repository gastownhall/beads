//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestReadyHidesChildrenOfUndatedDeferredParent pins GH#7300: a parent
// deferred with plain `bd defer` (status deferred, no defer_until) hides its
// children from `bd ready`, just as a parent with a future defer_until does.
func TestReadyHidesChildrenOfUndatedDeferredParent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	te := newTestEnv(t, "ud")

	for _, iss := range []*types.Issue{
		{ID: "ud-parent", Title: "undated deferred parent", Status: types.StatusDeferred, IssueType: types.TypeEpic},
		{ID: "ud-child", Title: "child of deferred", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "ud-free", Title: "free", Status: types.StatusOpen, IssueType: types.TypeTask},
	} {
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "ud-child", DependsOnID: "ud-parent", Type: types.DepParentChild}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	ready, err := te.store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	var ids []string
	for _, iss := range ready {
		ids = append(ids, iss.ID)
	}
	sawFree := false
	for _, id := range ids {
		if id == "ud-free" {
			sawFree = true
		}
		if id == "ud-child" || id == "ud-parent" {
			t.Errorf("%s listed as ready although its parent (or itself) is deferred: %v", id, ids)
		}
	}
	if !sawFree {
		t.Fatalf("ud-free missing from ready set %v", ids)
	}
}
