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

// TestUnDeferParentListsChildAgain pins the other side of GH#7300: hiding a
// plain `bd defer` parent's child is not a one-way door. Reopening the parent
// (status back to open) puts the child back on `bd ready`.
func TestUnDeferParentListsChildAgain(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	te := newTestEnv(t, "uu")

	for _, iss := range []*types.Issue{
		{ID: "uu-parent", Title: "undated deferred parent", Status: types.StatusDeferred, IssueType: types.TypeEpic},
		{ID: "uu-child", Title: "child of deferred", Status: types.StatusOpen, IssueType: types.TypeTask},
	} {
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "uu-child", DependsOnID: "uu-parent", Type: types.DepParentChild}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	idsOf := func(ready []*types.Issue) []string {
		out := make([]string, 0, len(ready))
		for _, iss := range ready {
			out = append(out, iss.ID)
		}
		return out
	}
	ready, err := te.store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	for _, iss := range ready {
		if iss.ID == "uu-child" {
			t.Fatalf("uu-child listed as ready while its parent is deferred: %v", idsOf(ready))
		}
	}

	if err := te.store.UpdateIssue(ctx, "uu-parent", map[string]interface{}{"status": string(types.StatusOpen), "defer_until": nil}, "tester"); err != nil {
		t.Fatalf("reopen uu-parent: %v", err)
	}

	ready, err = te.store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork after reopen: %v", err)
	}
	sawChild := false
	for _, iss := range ready {
		if iss.ID == "uu-child" {
			sawChild = true
		}
	}
	if !sawChild {
		t.Fatalf("uu-child missing from ready set after its parent reopened: %v", idsOf(ready))
	}
}
