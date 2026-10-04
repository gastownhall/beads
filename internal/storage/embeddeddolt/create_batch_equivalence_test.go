//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/createbatchequiv"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
)

// TestCreateBatchFastPathsMatchPerRow_Embedded runs the batch-create
// equivalence scenario (internal/storage/createbatchequiv) on the embedded
// engine: the same import-shaped batch through the fast and per-row bodies
// must store the same rows.
func TestCreateBatchFastPathsMatchPerRow_Embedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	createbatchequiv.Run(t, func(t *testing.T) *sql.DB {
		// Each fixture is its own directory, so both can carry the same
		// database name — and so the same issue_prefix for generated ids.
		fixture := newPristineEmbeddedDoltFixture(t, createbatchequiv.Prefix)
		t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })
		db, cleanup, err := openCountedConn(t.Context(), fixture.dataDir, fixture.database, &sqlcount.Counts{})
		if err != nil {
			t.Fatalf("openCountedConn: %v", err)
		}
		t.Cleanup(cleanup)
		return db
	})
}
