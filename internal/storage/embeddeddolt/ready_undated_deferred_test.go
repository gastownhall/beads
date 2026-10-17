//go:build cgo

package embeddeddolt_test

import (
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestReadyWorkHidesChildrenOfUndatedDeferredParent pins GH#7300: a parent put
// on ice with status 'deferred' and no defer_until hides its children from
// ready work, exactly as a future-dated deferred parent does. The store holds
// no row with a future defer_until, so the deferred-parent probe can only fire
// on the status.
func TestReadyWorkHidesChildrenOfUndatedDeferredParent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "ud")
	ctx := t.Context()
	create := func(id string, status types.Status, issueType types.IssueType) {
		t.Helper()
		issue := &types.Issue{ID: id, Title: id, Status: status, Priority: 2, IssueType: issueType}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
	create("ud-parent", types.StatusDeferred, types.TypeEpic)
	create("ud-child", types.StatusOpen, types.TypeTask)
	create("ud-free", types.StatusOpen, types.TypeTask)
	if err := te.store.AddDependency(ctx, &types.Dependency{
		IssueID: "ud-child", DependsOnID: "ud-parent", Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	filter := types.WorkFilter{}
	want := []string{"ud-free"}

	issues, err := te.store.GetReadyWork(ctx, filter)
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	var got []string
	for _, issue := range issues {
		got = append(got, issue.ID)
	}
	if !slices.Equal(got, want) {
		t.Errorf("GetReadyWork = %v, want %v", got, want)
	}

	withCounts, total, err := te.store.GetReadyWorkWithCountsAndTotal(ctx, filter)
	if err != nil {
		t.Fatalf("GetReadyWorkWithCountsAndTotal: %v", err)
	}
	if got := readyIDs(withCounts); !slices.Equal(got, want) || total != len(want) {
		t.Errorf("GetReadyWorkWithCountsAndTotal = %v (total %d), want %v (total %d)", got, total, want, len(want))
	}

	n, err := te.store.CountReadyWork(ctx, filter)
	if err != nil {
		t.Fatalf("CountReadyWork: %v", err)
	}
	if n != len(want) {
		t.Errorf("CountReadyWork = %d, want %d", n, len(want))
	}

	// The control: with the deferred filter lifted the child is listed again, so
	// the hiding above comes from the deferred parent and not from the fixture.
	all, err := te.store.GetReadyWork(ctx, types.WorkFilter{IncludeDeferred: true})
	if err != nil {
		t.Fatalf("GetReadyWork(IncludeDeferred): %v", err)
	}
	var inc []string
	for _, issue := range all {
		inc = append(inc, issue.ID)
	}
	if !slices.Contains(inc, "ud-child") {
		t.Errorf("GetReadyWork(IncludeDeferred) = %v, want it to list ud-child", inc)
	}
}
