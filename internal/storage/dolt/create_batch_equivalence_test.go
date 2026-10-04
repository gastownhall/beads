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
	createbatchequiv.Run(t, func(t *testing.T) *sql.DB {
		store, cleanup := setupConcurrentTestStore(t)
		t.Cleanup(cleanup)
		db, closeDB, err := openCountedDoltConn(store.connStr, &sqlcount.Counts{})
		if err != nil {
			t.Fatalf("openCountedDoltConn: %v", err)
		}
		t.Cleanup(closeDB)
		return db
	})
}
