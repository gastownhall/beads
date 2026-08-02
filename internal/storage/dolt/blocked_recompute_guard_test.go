package dolt

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// workingSetGraphDirty returns the committable graph tables (issues,
// dependencies) the guard reports as dirty.
func workingSetGraphDirty(ctx context.Context, t *testing.T, store *DoltStore) []string {
	t.Helper()
	rows, err := store.db.QueryContext(ctx,
		"SELECT DISTINCT table_name FROM dolt_status WHERE table_name IN ('issues','dependencies') ORDER BY table_name")
	if err != nil {
		t.Fatalf("query dolt_status: %v", err)
	}
	defer rows.Close()
	var dirty []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan dolt_status: %v", err)
		}
		dirty = append(dirty, name)
	}
	return dirty
}

// TestRecomputeAllBlocked_RefusesDirtyIssues is the bd-6dnrw.37 release-safety
// guard: a full recompute stages the whole `issues` table and derives flags
// from the current graph, so it must refuse to run while `issues` has unrelated
// uncommitted edits rather than sweep them into the repair commit.
func TestRecomputeAllBlocked_RefusesDirtyIssues(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true) // clean, consistent committed tree

	// Unrelated dirty edit the operator has not committed.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = 'uncommitted edit' WHERE id = 'bm-x'"); err != nil {
		t.Fatalf("dirty issues: %v", err)
	}

	changed, err := store.RecomputeAllBlocked(ctx)
	if !errors.Is(err, issueops.ErrBlockedRecomputeDirtyGraph) {
		t.Fatalf("want ErrBlockedRecomputeDirtyGraph, got changed=%d err=%v", changed, err)
	}
	if changed != 0 {
		t.Fatalf("refused recompute must report 0 rows, got %d", changed)
	}
	// The edit must remain uncommitted in the working set, not swept into a commit.
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 1 || dirty[0] != "issues" {
		t.Fatalf("issues edit must still be dirty (unswept), got dirty=%v", dirty)
	}
}

// TestRecomputeAllBlocked_RefusesDirtyDependencies guards the other half of the
// finding: is_blocked is derived from `dependencies`, so committing flags while
// dependency edits are uncommitted would publish state derived from a graph that
// is not part of the same commit.
func TestRecomputeAllBlocked_RefusesDirtyDependencies(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true)

	// Uncommitted dependency-graph edit.
	if _, err := store.db.ExecContext(ctx, "DELETE FROM dependencies WHERE issue_id = 'bm-w'"); err != nil {
		t.Fatalf("dirty dependencies: %v", err)
	}

	changed, err := store.RecomputeAllBlocked(ctx)
	if !errors.Is(err, issueops.ErrBlockedRecomputeDirtyGraph) {
		t.Fatalf("want ErrBlockedRecomputeDirtyGraph, got changed=%d err=%v", changed, err)
	}
	if changed != 0 {
		t.Fatalf("refused recompute must report 0 rows, got %d", changed)
	}
}

// TestRecomputeAllBlocked_AllowsDirtyWisps pins the deliberate exclusion of the
// dolt-ignored wisp tables from the guard. Dolt reports every ignored table as
// perpetually modified, so guarding on wisps would refuse the recompute in any
// workspace that uses them. A stale committed flag must still be repaired even
// while the wisp tables are dirty.
func TestRecomputeAllBlocked_AllowsDirtyWisps(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true)

	// A throwaway wisp leaves the dolt-ignored wisp tables perpetually dirty.
	scratch := &types.Issue{ID: "bm-wisp", Title: "scratch", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, scratch, "tester"); err != nil {
		t.Fatalf("create scratch issue: %v", err)
	}
	if err := store.UpdateIssue(ctx, "bm-wisp", map[string]interface{}{"no_history": true}, "tester"); err != nil {
		t.Fatalf("demote scratch to wisp: %v", err)
	}

	// Commit a stale is_blocked (the shape a skipped post-pull recompute leaves),
	// so the only remaining working-set dirt is the ignored wisp tables.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 0 WHERE id = 'bm-w'"); err != nil {
		t.Fatalf("corrupt is_blocked: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'commit stale flag + scratch demotion')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit stale flag: %v", err)
	}
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("precondition: committable graph must be clean, got dirty=%v", dirty)
	}

	// The recompute must run (not refuse) despite perpetually-dirty wisps.
	changed, err := store.RecomputeAllBlocked(ctx)
	if err != nil {
		t.Fatalf("recompute must run with only wisps dirty: %v", err)
	}
	if changed != 1 {
		t.Fatalf("want 1 row repaired, got %d", changed)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("bm-w must read blocked again after repair")
	}
}

// TestRecomputeAllBlocked_RepairsAndCommitsWhenClean covers the changed>0 commit
// branch of the store method: on a clean tree a stale flag is repaired and the
// correction is committed (the working set returns to clean), and the repair is
// idempotent.
func TestRecomputeAllBlocked_RepairsAndCommitsWhenClean(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedBlockedPair(ctx, t, store, true)

	// Commit a stale flag so the tree is clean going into the recompute.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET is_blocked = 0 WHERE id = 'bm-w'"); err != nil {
		t.Fatalf("corrupt is_blocked: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'commit stale flag')"); err != nil && !isDoltNothingToCommit(err) {
		t.Fatalf("commit stale flag: %v", err)
	}

	changed, err := store.RecomputeAllBlocked(ctx)
	if err != nil {
		t.Fatalf("recompute on clean tree: %v", err)
	}
	if changed != 1 {
		t.Fatalf("want 1 row repaired, got %d", changed)
	}
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatal("bm-w must read blocked again after repair")
	}
	// The repair was committed: the committable graph is clean again.
	if dirty := workingSetGraphDirty(ctx, t, store); len(dirty) != 0 {
		t.Fatalf("repair must be committed, leaving a clean tree, got dirty=%v", dirty)
	}
	// Idempotent: a second pass corrects nothing and stays clean.
	if again, err := store.RecomputeAllBlocked(ctx); err != nil || again != 0 {
		t.Fatalf("recompute must be idempotent: got changed=%d err=%v", again, err)
	}
}
