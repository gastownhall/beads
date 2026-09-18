//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestEmbeddedDependencyEditorVersionsOnlyARealAddOrRemove is the FOURTH
// ADDENDUM's own no-op-detection clause (design bead be-0uifx): the
// dependency editor is the one RecordVersionInTx caller not already
// short-circuited by DiscardNoopIssueUpdates, so it must draw its own R3
// exemption from the eventWritten signal AddDependencyInTx/
// RemoveDependencyInTx already return. This is an ENGINE CHECK on the shared
// internal/storage/issueops call sites (addDependencyEdgeInTx,
// ExecuteRemoveDependency) rather than a per-backend vote — see
// TestDualWriteContract's own doc comment for why one leg is proof for all
// three here, since dolt and uow route through the identical issueops
// functions.
//
// It asserts by DELTA against the source issue's own version-row count
// rather than an absolute value, so it stays correct regardless of how many
// rows the two CreateIssue seeds above it already minted.
func TestEmbeddedDependencyEditorVersionsOnlyARealAddOrRemove(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "depver")
	ctx := t.Context()

	configurer, ok := any(te.store).(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("%T does not implement storage.VersionedHistoryConfigurer", te.store)
	}
	configurer.SetVersionedHistoryEnabled(true)

	kit := newEmbeddedRoleFixtureKit(te, "depver")
	source := kit.IssuePrefix + "-src"
	target := kit.IssuePrefix + "-tgt"
	for _, id := range []string{source, target} {
		if err := kit.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "seed"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}

	editor, err := te.store.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}

	versionRowCount := func(id string) int {
		t.Helper()
		var count int
		if err := kit.QueryScalar(ctx, "SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", []any{id}, &count); err != nil {
			t.Fatalf("QueryScalar(issue_versions count for %s): %v", id, err)
		}
		return count
	}

	sourceBefore := versionRowCount(source)
	targetBefore := versionRowCount(target)

	if _, err := editor.AddDependencies(ctx, issueops.AddDependenciesRequest{
		Actor: "actor",
		Edges: []issueops.DependencyEdge{{IssueID: source, DependsOnID: target, Type: issueops.DepBlocks}},
	}); err != nil {
		t.Fatalf("AddDependencies (real add): %v", err)
	}
	afterAdd := versionRowCount(source)
	if afterAdd != sourceBefore+1 {
		t.Fatalf("real add: source version rows = %d, want %d (before %d)", afterAdd, sourceBefore+1, sourceBefore)
	}
	if got := versionRowCount(target); got != targetBefore {
		t.Fatalf("real add: target (non-referencing side) version rows = %d, want unchanged %d", got, targetBefore)
	}

	// Idempotent same-type re-add: AddDependencyInTx reports eventWritten
	// false, so this must not mint a second row (R3).
	if _, err := editor.AddDependencies(ctx, issueops.AddDependenciesRequest{
		Actor: "actor",
		Edges: []issueops.DependencyEdge{{IssueID: source, DependsOnID: target, Type: issueops.DepBlocks}},
	}); err != nil {
		t.Fatalf("AddDependencies (idempotent re-add): %v", err)
	}
	if got := versionRowCount(source); got != afterAdd {
		t.Fatalf("idempotent re-add: source version rows = %d, want unchanged %d", got, afterAdd)
	}

	if _, err := editor.RemoveDependency(ctx, issueops.RemoveDependencyRequest{
		Actor: "actor", IssueID: source, DependsOnID: target,
	}); err != nil {
		t.Fatalf("RemoveDependency (real remove): %v", err)
	}
	afterRemove := versionRowCount(source)
	if afterRemove != afterAdd+1 {
		t.Fatalf("real remove: source version rows = %d, want %d", afterRemove, afterAdd+1)
	}

	// Absent edge: RemoveDependencyInTx reports eventWritten false and
	// Removed false, so this must not mint a third row (R3).
	if _, err := editor.RemoveDependency(ctx, issueops.RemoveDependencyRequest{
		Actor: "actor", IssueID: source, DependsOnID: target,
	}); err != nil {
		t.Fatalf("RemoveDependency (already-absent): %v", err)
	}
	if got := versionRowCount(source); got != afterRemove {
		t.Fatalf("absent remove: source version rows = %d, want unchanged %d", got, afterRemove)
	}
}
