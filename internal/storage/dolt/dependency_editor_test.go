package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// What is left here is the DependencyEditor's PERSISTENCE tier: how a history
// entry lands and what it says. The role's semantics — result shape,
// idempotency verdicts, the typed refusals, all-or-nothing, and the number of
// history entries a call records — moved to
// conformance.RunDependencyEditor*, which runs on all three backends.
//
// These stay dolt-only on purpose. The commit-message spellings are
// single-sourced in internal/storage/issueops, so one backend pinning a
// spelling is enough and re-pinning it three times is duplication; the staging
// assertions read dolt_status and plant a dirty working set, and the working
// set is not a caller-visible concept on the unit-of-work route.

// TestDoltStoreDependencyEditorBatchNamesItsHistoryEntry pins the count
// spelling and the staging half: N edges commit as one entry that names the
// count, and the table it staged is clean afterwards.
func TestDoltStoreDependencyEditorBatchNamesItsHistoryEntry(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedIssues(ctx, t, store, "test-dep-add-a", "test-dep-add-b", "test-dep-add-c")

	editor, err := store.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	if _, err := editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{
			{IssueID: "test-dep-add-a", DependsOnID: "test-dep-add-b", Type: publicops.DepBlocks},
			{IssueID: "test-dep-add-a", DependsOnID: "test-dep-add-c", Type: publicops.DepRelated},
		},
	}); err != nil {
		t.Fatalf("AddDependencies: %v", err)
	}

	if !doltHasCommitMessage(ctx, t, store, "dependency: add 2 edges") {
		t.Fatal("no commit named the edge count")
	}
	// The batch's blocks edge flips test-dep-add-a's is_blocked, so `issues`
	// is one of the tables this commit must have taken with it.
	requireCleanTables(ctx, t, store, "issues", "dependencies")
}

// TestDoltStoreDependencyEditorBlockingAddCommitsIsBlocked is GH#6647 on the
// `bd dep add` path: the is_blocked flip a blocking edge writes for its source
// rides the same Dolt commit as the edge. Left unstaged, the flip sits in the
// working set (committed history says the bead is not blocked, and a peer pull
// or backup sees exactly that) until `bd recompute-blocked` refuses on the now
// dirty `issues` table.
func TestDoltStoreDependencyEditorBlockingAddCommitsIsBlocked(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedIssues(ctx, t, store, "test-dep-blk-a", "test-dep-blk-b")
	requireCleanTables(ctx, t, store, "issues", "dependencies")

	editor, err := store.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	if _, err := editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: "test-dep-blk-b", DependsOnID: "test-dep-blk-a", Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("AddDependencies: %v", err)
	}

	requireCleanTables(ctx, t, store, "issues", "dependencies")
	if !isBlockedAtHEAD(ctx, t, store, "test-dep-blk-b") {
		t.Fatal("committed HEAD has test-dep-blk-b is_blocked=0 despite an open blocker")
	}
	if _, err := store.RecomputeAllBlocked(ctx); err != nil {
		t.Fatalf("RecomputeAllBlocked after dep add: %v", err)
	}
}

// isBlockedAtHEAD reads is_blocked from the committed tree rather than the
// working set, which is what a peer pull, a branch diff, or a backup sees.
func isBlockedAtHEAD(ctx context.Context, t *testing.T, store *DoltStore, id string) bool {
	t.Helper()
	var blocked bool
	if err := store.db.QueryRowContext(ctx,
		"SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", id).Scan(&blocked); err != nil {
		t.Fatalf("read HEAD is_blocked of %s: %v", id, err)
	}
	return blocked
}

// TestDoltStoreDependencyEditorSingleEdgeNamesTheHistoryEntry pins the other
// spelling: one edge names both endpoints, however the caller spelled the
// request.
func TestDoltStoreDependencyEditorSingleEdgeNamesTheHistoryEntry(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedIssues(ctx, t, store, "test-dep-msg-a", "test-dep-msg-b")

	editor, err := store.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	if _, err := editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: "test-dep-msg-a", DependsOnID: "test-dep-msg-b", Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("AddDependencies single: %v", err)
	}
	if !doltHasCommitMessage(ctx, t, store, "bd: dep add test-dep-msg-a test-dep-msg-b") {
		t.Fatal("a single edge did not name both endpoints in history")
	}
}

// TestDoltStoreDependencyEditorRemoveNamesTheHistoryEntry pins the removal's
// spelling and its staging.
func TestDoltStoreDependencyEditorRemoveNamesTheHistoryEntry(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedIssues(ctx, t, store, "test-dep-rm-a", "test-dep-rm-b")

	editor, err := store.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	if _, err := editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: "test-dep-rm-a", DependsOnID: "test-dep-rm-b", Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("AddDependencies: %v", err)
	}
	if _, err := editor.RemoveDependency(ctx, publicops.RemoveDependencyRequest{
		Actor: "writer", IssueID: "test-dep-rm-a", DependsOnID: "test-dep-rm-b",
	}); err != nil {
		t.Fatalf("RemoveDependency: %v", err)
	}

	if !doltHasCommitMessage(ctx, t, store, "bd: dep remove test-dep-rm-a test-dep-rm-b") {
		t.Fatal("the removal did not name the edge in history")
	}
	// The removed edge unblocked test-dep-rm-a, so `issues` is one of the
	// tables this commit must have taken with it.
	requireCleanTables(ctx, t, store, "issues", "dependencies")
}

// TestDoltStoreDependencyEditorRemoveCommitsUnblock is GH#6647 on the
// `bd dep remove` path: removing the last blocker flips is_blocked back to 0
// in the same SQL transaction as the edge delete, and that flip must ride the
// removal's Dolt commit. Left unstaged, committed history keeps the bead
// blocked forever while the working set says otherwise.
func TestDoltStoreDependencyEditorRemoveCommitsUnblock(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedIssues(ctx, t, store, "test-dep-unblk-a", "test-dep-unblk-b")
	requireCleanTables(ctx, t, store, "issues", "dependencies")

	editor, err := store.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	if _, err := editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Actor: "writer",
		Edges: []publicops.DependencyEdge{{IssueID: "test-dep-unblk-b", DependsOnID: "test-dep-unblk-a", Type: publicops.DepBlocks}},
	}); err != nil {
		t.Fatalf("AddDependencies: %v", err)
	}
	// Start the removal from a committed tree where B is blocked: the remove
	// is then the only writer of the unblock flip.
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed blocked pair')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit seed: %v", err)
	}
	requireCleanTables(ctx, t, store, "issues", "dependencies")
	if !isBlockedAtHEAD(ctx, t, store, "test-dep-unblk-b") {
		t.Fatal("precondition: test-dep-unblk-b must be blocked at HEAD before the remove")
	}

	if _, err := editor.RemoveDependency(ctx, publicops.RemoveDependencyRequest{
		Actor: "writer", IssueID: "test-dep-unblk-b", DependsOnID: "test-dep-unblk-a",
	}); err != nil {
		t.Fatalf("RemoveDependency: %v", err)
	}

	requireCleanTables(ctx, t, store, "issues", "dependencies")
	if isBlockedAtHEAD(ctx, t, store, "test-dep-unblk-b") {
		t.Fatal("committed HEAD still has test-dep-unblk-b is_blocked=1 with no blocker left")
	}
}

// TestDoltStoreDependencyEditorNoOpRemoveLeavesPendingRowsUncommitted is the
// GH#2455 pin for this role: an operation that wrote nothing must stage
// nothing, because DOLT_ADD stages a whole TABLE and a commit made for a no-op
// carries away whatever else was pending in it.
//
// The commit COUNT cannot see this on its own — Dolt swallows a commit with an
// empty diff, so a leaky no-op only shows up when there is something in the
// table for it to sweep. That is why this case survives the conformance
// contract's own "a removal that found nothing records no history" assertion:
// they fail on different bugs.
func TestDoltStoreDependencyEditorNoOpRemoveLeavesPendingRowsUncommitted(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	seedIssues(ctx, t, store, "test-dep-sweep-a", "test-dep-sweep-b", "test-dep-sweep-c")

	editor, err := store.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	// A pending edge in the working set that belongs to nobody's transaction.
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by)
		VALUES (?, ?, ?, ?, NOW(), ?)`,
		"00000000-0000-4000-8000-00000000d09e", "test-dep-sweep-b", "test-dep-sweep-c", types.DepBlocks, "someone-else",
	); err != nil {
		t.Fatalf("stage a pending dependency row: %v", err)
	}

	before := doltCommitCount(ctx, t, store)
	removed, err := editor.RemoveDependency(ctx, publicops.RemoveDependencyRequest{
		Actor: "writer", IssueID: "test-dep-sweep-a", DependsOnID: "test-dep-sweep-b",
	})
	if err != nil {
		t.Fatalf("RemoveDependency on a missing edge: %v", err)
	}
	if removed.Removed {
		t.Fatal("Removed = true, want false")
	}
	if after := doltCommitCount(ctx, t, store); after != before {
		t.Fatalf("commit count = %d, want %d: the no-op swept the pending row into a commit", after, before)
	}
	var dirty int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'dependencies'").Scan(&dirty); err != nil {
		t.Fatalf("query dolt_status: %v", err)
	}
	if dirty == 0 {
		t.Fatal("dependencies is clean: the no-op removal committed a row it never wrote")
	}
}
