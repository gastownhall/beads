package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// TestRecomputeIsBlockedInTx_DoltDialect is the go-mysql-server / Dolt
// (MySQL-dialect) companion to TestRecomputeIsBlockedInTx_SQLiteGrammar in
// internal/storage/issueops. The is_blocked recompute templates are shared by
// both the embedded (SQLite/DoltLite) and server/proxied (MySQL/gms) backends
// (see the DBTX doc comment in blocked_state.go), so de-aliasing the UPDATE
// targets to fix the SQLite grammar must not regress the MySQL path — in
// particular it must not re-trigger MySQL ERROR 1093 on the same-table EXISTS
// subqueries. This drives the de-aliased mark and unmark templates against the
// real Dolt engine.
func TestRecomputeIsBlockedInTx_DoltDialect(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	// mark: bm-w blocked on open bm-x, established through the normal write
	// path (AddDependency), which runs the shared mark template on Dolt.
	seedBlockedPair(ctx, t, store, true)
	if !isBlocked(ctx, t, store.db, "bm-w") {
		t.Fatalf("bm-w should be blocked by open bm-x (mark template on Dolt)")
	}
	if isBlocked(ctx, t, store.db, "bm-x") {
		t.Fatalf("bm-x blocks others but is not itself blocked")
	}

	// unmark: close the blocker, then drive the shared recompute directly so
	// the de-aliased unmark template runs against Dolt and clears the flag.
	if _, err := store.db.ExecContext(ctx, "UPDATE issues SET status = 'closed' WHERE id = ?", "bm-x"); err != nil {
		t.Fatalf("close blocker: %v", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin recompute tx: %v", err)
	}
	if err := issueops.RecomputeIsBlockedInTx(ctx, tx, []string{"bm-w", "bm-x"}, nil); err != nil {
		_ = tx.Rollback()
		t.Fatalf("RecomputeIsBlockedInTx on Dolt (regression: de-alias must not break MySQL): %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit recompute tx: %v", err)
	}
	if isBlocked(ctx, t, store.db, "bm-w") {
		t.Errorf("bm-w should be unblocked after bm-x closed (unmark template on Dolt)")
	}
}
