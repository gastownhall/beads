package dolt

import (
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/createbatchequiv"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
)

// TestCreateBatchFastPathsMatchPerRow_Dolt runs the batch-create equivalence
// scenario (internal/storage/createbatchequiv) on the Dolt server backend:
// the same import-shaped batch through the fast and per-row bodies must store
// the same rows.
func TestCreateBatchFastPathsMatchPerRow_Dolt(t *testing.T) {
	createbatchequiv.Run(t, openEquivalenceDB)
}

// TestCreateBatchFastPathsMatchPerRowLarge_Dolt runs the 458-issue scenarios
// (about four minutes on this backend), as a top-level test of its own so the
// full suite's shard manifest places it apart from the light scenarios.
func TestCreateBatchFastPathsMatchPerRowLarge_Dolt(t *testing.T) {
	createbatchequiv.RunLarge(t, openEquivalenceDB)
}

func openEquivalenceDB(t *testing.T) *sql.DB {
	t.Helper()
	store, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup)
	// Generated ids carry the configured prefix; match the embedded
	// fixture's so both engines reproduce the same golden digests.
	if err := store.SetConfig(t.Context(), "issue_prefix", createbatchequiv.Prefix); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	db, closeDB, err := openCountedDoltConn(store.connStr, &sqlcount.Counts{})
	if err != nil {
		t.Fatalf("openCountedDoltConn: %v", err)
	}
	t.Cleanup(closeDB)
	return db
}
