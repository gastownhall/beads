package dolt

import (
	"testing"
)

// TestPinStoreBranch_ReproducesStoreActiveBranch is the be-b0am regression
// test: pinStoreBranch is the single implementation withReadTxLongTimeout,
// recomputeAllBlocked, and recomputeBlockedTx all depend on to avoid running
// on a fresh connection's default branch instead of the store's real branch.
// Branch checkout is connection-scoped session state in Dolt, so a fresh
// *sql.DB from openLongTimeoutConn defaults away from whatever the pooled
// store connection (s.db) is actually checked out to — this is exactly the
// production bug reported against recomputeAllBlocked/recomputeBlockedTx.
//
// This asserts the invariant directly on the connection's active_branch(),
// not on a downstream symptom (row counts, commit contents). A symptom-only
// test would pass again the moment someone reintroduces the split with
// different pool timing — see federation.go's SetMaxIdleConns(0)+Conn(ctx)
// pattern for why that hazard is not hypothetical.
func TestPinStoreBranch_ReproducesStoreActiveBranch(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	var storeBranch string
	if err := store.db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&storeBranch); err != nil {
		t.Fatalf("query store active_branch: %v", err)
	}
	if storeBranch == "" || storeBranch == "main" {
		t.Fatalf("precondition: setupTestStore must isolate the pooled connection onto a non-default branch, got %q", storeBranch)
	}

	db, err := store.openLongTimeoutConn()
	if err != nil {
		t.Fatalf("openLongTimeoutConn: %v", err)
	}
	defer db.Close()

	var freshBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&freshBranch); err != nil {
		t.Fatalf("query fresh connection active_branch: %v", err)
	}
	if freshBranch == storeBranch {
		t.Fatalf("precondition: a brand-new connection must NOT already report the store's branch (got %q) — otherwise this test cannot distinguish a pinned connection from an accidentally-matching default", storeBranch)
	}

	if err := store.pinStoreBranch(ctx, db); err != nil {
		t.Fatalf("pinStoreBranch: %v", err)
	}

	var pinnedBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&pinnedBranch); err != nil {
		t.Fatalf("query pinned connection active_branch: %v", err)
	}
	if pinnedBranch != storeBranch {
		t.Fatalf("connection must report the store's active branch %q after pinStoreBranch, got %q", storeBranch, pinnedBranch)
	}
}
