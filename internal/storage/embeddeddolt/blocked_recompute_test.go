//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedRecomputeAllBlockedWiring confirms the embedded store satisfies
// the cross-mode BlockedRecomputer capability (bd-6dnrw.37) and that a full
// recompute over a correctly-maintained graph is a clean no-op — the path that
// runs whenever 'bd recompute-blocked' is invoked in embedded mode.
//
// The is_blocked SQL semantics (stale-flag detection, repair, cascade,
// idempotence) are exercised against a real engine by the dolt package's
// RecomputeAllIsBlocked lockstep tests, which share the exact issueops core;
// the embedded commit path (StageAndCommit of "issues") is the same one the
// already-tested recomputeBlockedAfterPull uses.
func TestEmbeddedRecomputeAllBlockedWiring(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "rcb")
	ctx := t.Context()

	// Correct graph via the normal write path: rcb-w blocked on open rcb-x.
	for _, id := range []string{"rcb-w", "rcb-x"} {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "rcb-w", DependsOnID: "rcb-x", Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("add dependency: %v", err)
	}
	// Embedded writes land in the working set; production flushes them to Dolt
	// history on session shutdown, so `bd recompute-blocked` (a fresh process)
	// sees a clean tree. Commit here to match that precondition — the recompute's
	// dirty-graph guard refuses an uncommitted issues/dependencies tree
	// (bd-6dnrw.37).
	if err := te.store.Commit(ctx, "seed consistent graph"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	rc, ok := storage.UnwrapStore(te.store).(storage.BlockedRecomputer)
	if !ok {
		t.Fatal("embedded store must implement storage.BlockedRecomputer")
	}

	// A correctly-maintained graph needs no corrections, and the recompute must
	// be idempotent.
	changed, err := rc.RecomputeAllBlocked(ctx)
	if err != nil {
		t.Fatalf("RecomputeAllBlocked: %v", err)
	}
	if changed != 0 {
		t.Fatalf("consistent graph: want 0 rows corrected, got %d", changed)
	}
	if again, err := rc.RecomputeAllBlocked(ctx); err != nil || again != 0 {
		t.Fatalf("recompute must stay a no-op: got changed=%d err=%v", again, err)
	}

	// The same repair through the library entry bd recompute-blocked uses
	// (storage.RecomputeBlocked), which also asks the store how many rows it
	// left as found. rcb-a is blocked on a wisp; with the wisp's row deleted
	// by hand and its edge kept, which is what a clone of this database
	// holds, the repair leaves rcb-a's flag alone and counts it.
	if _, ok := storage.UnwrapStore(te.store).(storage.BlockedRecomputeCounter); !ok {
		t.Fatal("embedded store must implement storage.BlockedRecomputeCounter")
	}
	for _, iss := range []*types.Issue{
		{ID: "rcb-a", Title: "rcb-a", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "rcb-wisp", Title: "rcb-wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
	} {
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", iss.ID, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "rcb-a", DependsOnID: "rcb-wisp", Type: types.DepBlocks}, "tester"); err != nil {
		t.Fatalf("add dependency on the wisp: %v", err)
	}
	if err := te.store.Commit(ctx, "seed an issue blocked on a wisp"); err != nil {
		t.Fatalf("commit wisp seed: %v", err)
	}
	if got, err := storage.RecomputeBlocked(ctx, te.store); err != nil || got != (storage.RecomputeBlockedResult{}) {
		t.Fatalf("wisp present: RecomputeBlocked = (%+v, %v), want the zero result", got, err)
	}

	te.exec(t, ctx, "DELETE FROM wisps WHERE id = ?", "rcb-wisp")
	te.assertRowNotExists(t, ctx, "wisps", "rcb-wisp")
	got, err := storage.RecomputeBlocked(ctx, te.store)
	if err != nil {
		t.Fatalf("wisp absent: RecomputeBlocked: %v", err)
	}
	if want := (storage.RecomputeBlockedResult{RowsAbstained: 1}); got != want {
		t.Fatalf("wisp absent: RecomputeBlocked = %+v, want %+v", got, want)
	}
	var flag bool
	te.queryScalar(t, ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{"rcb-a"}, &flag)
	if !flag {
		t.Error("rcb-a was cleared on a guess about a wisp this database does not have")
	}
}

// TestEmbeddedRecomputeAllBlockedDirtyGraphIsTyped pins the EMBEDDED half of
// the guard's error contract: RecomputeAllBlocked must return an error that
// errors.Is-matches issueops.ErrBlockedRecomputeDirtyGraph, not a re-created or
// stringified one.
//
// 'bd sync' classifies this condition as retryable from that sentinel alone
// (cmd/bd/sync.go, isRecomputeDirtyGraphErr, wy-mlnz2). The embedded path
// returns the guard error through withConn's errors.Join, which preserves the
// chain today — but nothing failed if someone rewrote that as a %v wrap, which
// would silently demote a concurrent writer's transient dirty working set back
// to a hard exit-1 sync failure on every embedded rig. The server-mode half is
// pinned by internal/storage/dolt/blocked_recompute_guard_test.go.
func TestEmbeddedRecomputeAllBlockedDirtyGraphIsTyped(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "rcbdirty")
	ctx := t.Context()

	// An uncommitted write leaves `issues` dirty in the working set — exactly
	// the state a concurrent writer produces mid-transaction.
	iss := &types.Issue{ID: "rcbdirty-a", Title: "dirty", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
		t.Fatalf("create: %v", err)
	}

	rc, ok := storage.UnwrapStore(te.store).(storage.BlockedRecomputer)
	if !ok {
		t.Fatal("embedded store must implement storage.BlockedRecomputer")
	}
	changed, err := rc.RecomputeAllBlocked(ctx)
	if !errors.Is(err, issueops.ErrBlockedRecomputeDirtyGraph) {
		t.Fatalf("want ErrBlockedRecomputeDirtyGraph, got changed=%d err=%v", changed, err)
	}
}
