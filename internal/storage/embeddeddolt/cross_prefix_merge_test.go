//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// These tests pin the merge half of the GH#8299 arrival re-file. A merge
// brings in writes no local write path ran for, so either half of an edge
// filed external because its target was not here can arrive by a pull: the
// target, which another clone created, or the row, which another clone wrote
// before it held the target. After the merge the edge must gate its source
// as it does after a create. The peer is a branch of the same database, as in
// the merge-settle tests: a pull merges the remote tracking ref through the
// same code, and only the fetch is skipped.

// commitOnPeerBranch checks out branch on its own short-lived session, runs
// write there and commits it: what another clone wrote, which reaches this
// one only through a merge, past every local write path. The session closes
// before the store runs again, so the store stays on main.
func commitOnPeerBranch(t *testing.T, ctx context.Context, te *testEnv, branch string, write func(conn *sql.Conn)) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer conn.Close()
	execFrozenGuard(t, ctx, conn, "CALL DOLT_CHECKOUT('"+branch+"')")
	write(conn)
	execFrozenGuard(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'peer writes')")
}

// mergePeer merges branch into main through the store, the path `bd vc
// merge` takes and a pull's merge shares, and requires it conflict-free.
func mergePeer(t *testing.T, ctx context.Context, te *testEnv, branch string) {
	t.Helper()
	conflicts, err := te.store.Merge(ctx, branch)
	if err != nil {
		t.Fatalf("merge %s: %v", branch, err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("merge %s: %d conflict(s), want a clean merge", branch, len(conflicts))
	}
}

// requireGraphCommitted asserts the merge left no versioned graph change
// uncommitted: the re-filed rows and blocked flags are in the commit the
// merge's recompute makes.
func requireGraphCommitted(t *testing.T, ctx context.Context, te *testEnv) {
	t.Helper()
	var dirty int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name IN ('issues', 'dependencies')", nil, &dirty)
	if dirty != 0 {
		t.Errorf("%d graph table(s) left uncommitted after the merge", dirty)
	}
}

func createCrossPrefixFixtureIssue(t *testing.T, ctx context.Context, te *testEnv, id string, ephemeral bool) {
	t.Helper()
	issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue %s: %v", id, err)
	}
}

// commitStore commits the store's working set, as each bd command does
// before it exits, so a branch cut next and the merge both see it.
func commitStore(t *testing.T, ctx context.Context, te *testEnv, message string) {
	t.Helper()
	if err := te.store.Commit(ctx, message); err != nil {
		t.Fatalf("commit %q: %v", message, err)
	}
}

// TestEmbeddedCrossPrefixMergeFilesEdgesOntoAMergedTarget: this clone adds
// an edge, from an issue and from a wisp, onto an id it does not hold yet, so
// both rows are filed external; a peer creates that id. Once the merge brings
// the issue in, both edges are filed into depends_on_issue_id, their sources
// are blocked, the issue leaves ready work, and the moved durable row is in
// the merge's commit.
func TestEmbeddedCrossPrefixMergeFilesEdgesOntoAMergedTarget(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	const target, source, wispSource, peer = "b-mtgt1", "a-msrc1", "a-mwsrc1", "merge-target-peer"
	createCrossPrefixFixtureIssue(t, ctx, te, source, false)
	createCrossPrefixFixtureIssue(t, ctx, te, wispSource, true)
	commitStore(t, ctx, te, "sources")
	te.exec(t, ctx, "CALL DOLT_BRANCH('"+peer+"', 'HEAD')")

	for _, src := range []string{source, wispSource} {
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: target, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", src, target, err)
		}
	}
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", source, target, "depends_on_external")
	assertCrossPrefixEdgeColumn(t, ctx, te, "wisp_dependencies", wispSource, target, "depends_on_external")
	commitStore(t, ctx, te, "edges onto an id not held yet")

	commitOnPeerBranch(t, ctx, te, peer, func(conn *sql.Conn) {
		seedLegacyIssue(t, ctx, conn, "issues", target, "open", 1)
	})
	mergePeer(t, ctx, te, peer)

	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", source, target, "depends_on_issue_id")
	assertCrossPrefixEdgeColumn(t, ctx, te, "wisp_dependencies", wispSource, target, "depends_on_issue_id")
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", source, 1)
	assertCrossPrefixIsBlocked(t, ctx, te, "wisps", wispSource, 1)
	if crossPrefixReady(t, ctx, te, source) {
		t.Errorf("%s is ready beside its open blocker %s, which a merge brought in", source, target)
	}
	requireGraphCommitted(t, ctx, te)
}

// TestEmbeddedCrossPrefixMergeFilesAMergedEdgeOntoAHeldTarget: a peer that
// did not hold the target, or a binary from before the fix, files an edge
// external; the merge brings that row onto a clone that holds the target, so
// the merge files it into depends_on_issue_id and its source is blocked. The
// rules of the write path still hold: a blocking edge onto the source's own
// ancestor stays external, and a synced row whose target is only a wisp of
// this clone stays external, since a clone-local wisp must not decide how
// every clone files a synced row. For the same reason a wisp of this clone
// in the source's hierarchy does not count: an edge onto an issue that is an
// ancestor only through such a wisp is filed local, as a clone without the
// wisp files it.
func TestEmbeddedCrossPrefixMergeFilesAMergedEdgeOntoAHeldTarget(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	const (
		target, source                 = "b-mheld1", "a-medge1"
		wisp, ontoWisp                 = "b-mwisp1", "a-montow1"
		grand, parent, child           = "b-mgrand1", "a-mpar1", "a-mkid1"
		wispTop, wispParent, belowWisp = "b-mwtop1", "b-mwpar1", "a-mwkid1"
		peer                           = "merge-edge-peer"
		blocks                         = string(types.DepBlocks)
		externalColumn, issues         = "depends_on_external", "issues"
	)
	for _, id := range []string{target, source, ontoWisp, grand, parent, child, wispTop, belowWisp} {
		createCrossPrefixFixtureIssue(t, ctx, te, id, false)
	}
	createCrossPrefixFixtureIssue(t, ctx, te, wisp, true)
	createCrossPrefixFixtureIssue(t, ctx, te, wispParent, true)
	for _, dep := range []*types.Dependency{
		{IssueID: parent, DependsOnID: grand, Type: types.DepParentChild},
		{IssueID: child, DependsOnID: parent, Type: types.DepParentChild},
		{IssueID: wispParent, DependsOnID: wispTop, Type: types.DepParentChild},
		{IssueID: belowWisp, DependsOnID: wispParent, Type: types.DepParentChild},
	} {
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}
	commitStore(t, ctx, te, "held targets and hierarchy")
	te.exec(t, ctx, "CALL DOLT_BRANCH('"+peer+"', 'HEAD')")

	commitOnPeerBranch(t, ctx, te, peer, func(conn *sql.Conn) {
		seedDep(t, ctx, conn, "dependencies", source, target, externalColumn, blocks)
		seedDep(t, ctx, conn, "dependencies", ontoWisp, wisp, externalColumn, blocks)
		seedDep(t, ctx, conn, "dependencies", child, grand, externalColumn, blocks)
		seedDep(t, ctx, conn, "dependencies", belowWisp, wispTop, externalColumn, blocks)
	})
	mergePeer(t, ctx, te, peer)

	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", source, target, "depends_on_issue_id")
	assertCrossPrefixIsBlocked(t, ctx, te, issues, source, 1)
	if crossPrefixReady(t, ctx, te, source) {
		t.Errorf("%s is ready beside its open blocker %s after merging the edge in", source, target)
	}
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", child, grand, externalColumn)
	assertCrossPrefixIsBlocked(t, ctx, te, issues, child, 0)
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", ontoWisp, wisp, externalColumn)
	assertCrossPrefixIsBlocked(t, ctx, te, issues, ontoWisp, 0)
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", belowWisp, wispTop, "depends_on_issue_id")
	assertCrossPrefixIsBlocked(t, ctx, te, issues, belowWisp, 1)
	requireGraphCommitted(t, ctx, te)
}

// TestEmbeddedCrossPrefixFullMergeRecomputeFilesHeldTargets: when the merge
// cannot be scoped (here the pre-merge HEAD is unknown, as after a failed
// read), the full pass files every external edge onto a held target, by the
// same rules as the scoped one, and commits it: a synced row onto a wisp of
// this clone stays external, and one onto an issue that is the source's
// ancestor only through such a wisp is filed local.
func TestEmbeddedCrossPrefixFullMergeRecomputeFilesHeldTargets(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()

	const target, source, wisp, ontoWisp = "b-fheld1", "a-fsrc1", "b-fwisp1", "a-fontow1"
	const wispTop, wispParent, belowWisp = "b-fwtop1", "b-fwpar1", "a-fwkid1"
	for _, id := range []string{target, source, ontoWisp, wispTop, belowWisp} {
		createCrossPrefixFixtureIssue(t, ctx, te, id, false)
	}
	createCrossPrefixFixtureIssue(t, ctx, te, wisp, true)
	createCrossPrefixFixtureIssue(t, ctx, te, wispParent, true)
	for _, dep := range []*types.Dependency{
		{IssueID: wispParent, DependsOnID: wispTop, Type: types.DepParentChild},
		{IssueID: belowWisp, DependsOnID: wispParent, Type: types.DepParentChild},
	} {
		if err := te.store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}
	seedExternalCrossPrefixEdge(t, ctx, te, "dependencies", source, target, types.DepBlocks)
	seedExternalCrossPrefixEdge(t, ctx, te, "dependencies", ontoWisp, wisp, types.DepBlocks)
	seedExternalCrossPrefixEdge(t, ctx, te, "dependencies", belowWisp, wispTop, types.DepBlocks)
	te.exec(t, ctx, "CALL DOLT_COMMIT('-Am', 'merged: external edges onto held targets')")

	if err := te.store.RecomputeBlockedAfterMerge(ctx, ""); err != nil {
		t.Fatalf("RecomputeBlockedAfterMerge with an unknown pre-merge HEAD: %v", err)
	}
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", source, target, "depends_on_issue_id")
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", source, 1)
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", ontoWisp, wisp, "depends_on_external")
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", ontoWisp, 0)
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", belowWisp, wispTop, "depends_on_issue_id")
	assertCrossPrefixIsBlocked(t, ctx, te, "issues", belowWisp, 1)
	requireGraphCommitted(t, ctx, te)
}
