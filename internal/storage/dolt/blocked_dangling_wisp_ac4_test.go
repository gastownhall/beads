package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDanglingWispEdge_PresentWispStillDecides pins what must not change: a
// wisp that IS here decides its dependents both ways, on both paths.
func TestDanglingWispEdge_PresentWispStillDecides(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	createPerm(t, ctx, store, "dwp-a")
	createWisp(t, ctx, store, "dwp-w")
	addDependencyWithMeta(t, ctx, store, "dwp-a", "dwp-w", types.DepBlocks, "")
	assertBothPathsSet(ctx, t, store, "present open wisp",
		map[string]bool{"dwp-a": false}, map[string]bool{"dwp-a": true}, "dwp-a")

	if _, err := store.db.ExecContext(ctx, "UPDATE wisps SET status = 'closed' WHERE id = 'dwp-w'"); err != nil {
		t.Fatalf("close wisp by hand: %v", err)
	}
	if n := countInconsistencies(ctx, t, store.db); n != 1 {
		t.Errorf("present closed wisp: doctor counts %d, want 1", n)
	}
	assertBothPathsSet(ctx, t, store, "present closed wisp",
		map[string]bool{"dwp-a": true}, map[string]bool{"dwp-a": false}, "dwp-a")
}
