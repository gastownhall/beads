//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
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
}
