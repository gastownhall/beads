//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// assertDepTargetColumn asserts which typed target column the single edge
// source -> target occupies in table, and that it occupies exactly one.
func assertDepTargetColumn(t *testing.T, te *testEnv, table, source, target, wantColumn string) {
	t.Helper()
	var issueTgt, wispTgt, external string
	te.queryScalar(t, t.Context(),
		"SELECT COALESCE(depends_on_issue_id, ''), COALESCE(depends_on_wisp_id, ''), COALESCE(depends_on_external, '') FROM "+table+" WHERE issue_id = ?",
		[]any{source}, &issueTgt, &wispTgt, &external)
	got := map[string]string{
		"depends_on_issue_id": issueTgt,
		"depends_on_wisp_id":  wispTgt,
		"depends_on_external": external,
	}
	for column, id := range got {
		want := ""
		if column == wantColumn {
			want = target
		}
		if id != want {
			t.Errorf("%s.%s for edge %s -> %s = %q, want %q", table, column, source, target, id, want)
		}
	}
}

// createCrossPrefixPair seeds an open source issue and an open local target
// issue whose id carries a different prefix than the source's (GH#8299's
// shape: both rows live in this database).
func createCrossPrefixPair(t *testing.T, te *testEnv, source, target string) {
	t.Helper()
	for _, id := range []string{source, target} {
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(t.Context(), issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
	}
}

// TestEmbeddedCrossPrefixLocalTargetUsesIssueColumn pins the write-side half
// of GH#8299: a dependency whose target exists in this database is a local
// edge whatever its id prefix, so it must occupy depends_on_issue_id — the
// column whose foreign key protects it from a later delete and whose value
// the blocked-state and readiness reads resolve. A prefix mismatch alone must
// not route it to depends_on_external, where no local reader ever sees it.
// Both the store verb and the transactional write path are exercised, because
// they reach the classification seam through different call frames.
func TestEmbeddedCrossPrefixLocalTargetUsesIssueColumn(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	const dependent, blocker = "a-x1", "b-y1"
	createCrossPrefixPair(t, te, dependent, blocker)

	if err := te.store.AddDependency(ctx, &types.Dependency{
		IssueID: dependent, DependsOnID: blocker, Type: types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	assertDepTargetColumn(t, te, "dependencies", dependent, blocker, "depends_on_issue_id")

	// The transactional write leg (bd connect-style callers) classifies the
	// same edge through its own frame; it must agree with the store verb.
	const txSource, txTarget = "a-x2", "b-y2"
	createCrossPrefixPair(t, te, txSource, txTarget)
	if err := te.store.RunInTransaction(ctx, "test: cross-prefix local edge", func(tx storage.Transaction) error {
		return tx.AddDependency(ctx, &types.Dependency{
			IssueID: txSource, DependsOnID: txTarget, Type: types.DepBlocks,
		}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction AddDependency: %v", err)
	}
	assertDepTargetColumn(t, te, "dependencies", txSource, txTarget, "depends_on_issue_id")
}
