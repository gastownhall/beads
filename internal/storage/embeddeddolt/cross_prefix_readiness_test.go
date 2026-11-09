//go:build cgo

package embeddeddolt_test

import (
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedCrossPrefixLocalBlocksReadiness pins the read-side half of
// GH#8299: a blocks edge whose target exists in this database under a
// different id prefix is a local edge, so it must actually block. The source
// starts blocked and is excluded from ready work, the blocked-issues
// explanation (the storage feed of `bd ready --explain`'s "blocked by" lines)
// names the cross-prefix target, and closing that target releases the source
// with is_blocked cleared. On base, the edge was filed in depends_on_external
// and none of this held: the source sat ready with an untracked blocker.
func TestEmbeddedCrossPrefixLocalBlocksReadiness(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	const dependent, blocker = "a-r1", "b-r2"
	createCrossPrefixPair(t, te, dependent, blocker)
	if err := te.store.AddDependency(ctx, &types.Dependency{
		IssueID: dependent, DependsOnID: blocker, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	assertDepTargetColumn(t, te, "dependencies", dependent, blocker, "depends_on_issue_id")

	// While the local cross-prefix blocker is open the source is blocked,
	// fenced out of ready work, and explained by the blocker's id.
	var isBlocked bool
	te.queryScalar(t, ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{dependent}, &isBlocked)
	if !isBlocked {
		t.Errorf("is_blocked for %s = false while open local cross-prefix blocker %s exists", dependent, blocker)
	}
	ready, err := te.store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if slices.ContainsFunc(ready, func(i *types.Issue) bool { return i.ID == dependent }) {
		t.Errorf("%s listed as ready while blocked by open local cross-prefix issue %s", dependent, blocker)
	}
	blocked, err := te.store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues: %v", err)
	}
	var blockedBy []string
	for _, bi := range blocked {
		if bi.ID == dependent {
			blockedBy = bi.BlockedBy
		}
	}
	if !slices.Contains(blockedBy, blocker) {
		t.Errorf("blocked-issues explanation for %s = %v, want blocker list naming %s", dependent, blockedBy, blocker)
	}

	// Closing the local cross-prefix target releases the source.
	if err := te.store.CloseIssue(ctx, blocker, "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue %s: %v", blocker, err)
	}
	te.queryScalar(t, ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{dependent}, &isBlocked)
	if isBlocked {
		t.Errorf("is_blocked for %s = true after its only blocker %s closed", dependent, blocker)
	}
	ready, err = te.store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork after close: %v", err)
	}
	if !slices.ContainsFunc(ready, func(i *types.Issue) bool { return i.ID == dependent }) {
		t.Errorf("%s missing from ready work after its only blocker %s closed", dependent, blocker)
	}
}
