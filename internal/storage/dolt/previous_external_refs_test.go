package dolt

import (
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestPreviousExternalRefsMatchesPerIssueLookup covers AC-5 on the server
// backend, on the test's own non-main branch: the batch read returns exactly
// what the per-issue PreviousExternalRef returns over the shared
// external_ref history scenario (see testutil.BuildExternalRefHistoryScenario).
func TestPreviousExternalRefsMatchesPerIssueLookup(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	// The store's pool is one connection (setupTestStore), so the scenario
	// is built on that session's branch and the connection is handed back
	// before the store reads.
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	scenario := testutil.BuildExternalRefHistoryScenario(t, ctx, conn, "perh")
	if err := conn.Close(); err != nil {
		t.Fatalf("release connection: %v", err)
	}

	testutil.AssertPreviousExternalRefsParity(t, ctx, store, scenario, "perh")
	testutil.AssertExternalRefHistoryDiscriminator(t, ctx, store, scenario, "perh")
	testutil.AssertExternalRefHistoryOutOfOrderMerge(t, ctx, store, scenario, "perh")
}

// TestPreviousExternalRefsUsesDedicatedLongTimeoutConnection: the batch read
// is a dolt_history_issues scan like History's, so it must run in the same
// long-timeout history transaction rather than on the shared pool and its
// 10s ReadTimeout. Same technique as
// TestHistory_UsesDedicatedLongTimeoutConnection.
func TestPreviousExternalRefsUsesDedicatedLongTimeoutConnection(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := store.PreviousExternalRefs(ctx, []string{"perh-1"}, time.Now()); err != nil {
		t.Fatalf("PreviousExternalRefs before connStr corruption: %v", err)
	}

	cfg, err := mysql.ParseDSN(store.connStr)
	if err != nil {
		t.Fatalf("failed to parse store.connStr: %v", err)
	}
	cfg.Addr = "bd-84vbqq-test.invalid:3306"
	store.connStr = cfg.FormatDSN()

	if _, err := store.PreviousExternalRefs(ctx, []string{"perh-1"}, time.Now()); err == nil {
		t.Fatal("PreviousExternalRefs succeeded with store.connStr broken: it read through the shared pool instead of its dedicated long-timeout connection")
	}
}
