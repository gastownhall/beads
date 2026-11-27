package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// bd-muhnf0 repro: on a clone, dependencies (versioned) carries an edge to a
// wisp whose row is absent (wisps is dolt_ignored). The full is_blocked
// repair must not read the absent wisp as a satisfied blocker and clear the
// dependent's flag; it should leave is_blocked as found.
func TestRecomputeAllIsBlocked_DanglingWispEdgeLeavesFlagAsFound(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createWisp(t, ctx, store, "zz-dw")
	createPerm(t, ctx, store, "zz-da")
	addDependencyWithMeta(t, ctx, store, "zz-da", "zz-dw", types.DepBlocks, "")
	if !readIsBlockedFlags(t, ctx, store, "issues")["zz-da"] {
		t.Fatal("fixture: zz-da should be blocked by open wisp zz-dw")
	}

	// Simulate the clone: the wisp row is not here, the edge is.
	if _, err := store.db.ExecContext(ctx, "DELETE FROM wisps WHERE id = 'zz-dw'"); err != nil {
		t.Fatalf("drop wisp row: %v", err)
	}
	var edges int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = 'zz-da' AND depends_on_wisp_id = 'zz-dw'").Scan(&edges); err != nil {
		t.Fatalf("count edge: %v", err)
	}
	if edges != 1 {
		t.Fatalf("fixture: dangling edge should remain, got %d rows", edges)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("RecomputeAllIsBlockedInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if !readIsBlockedFlags(t, ctx, store, "issues")["zz-da"] {
		t.Fatal("full repair cleared is_blocked on zz-da: the absent wisp blocker was read as satisfied; want flag left as found (1)")
	}
}
