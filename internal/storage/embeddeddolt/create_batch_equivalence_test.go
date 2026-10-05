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
	createbatchequiv.Run(t, openEquivalenceDB)
}

// TestCreateBatchFastPathsMatchPerRowLarge_Embedded runs the 458-issue
// equivalence scenarios. It is skipped under -race, where the in-process
// engine's own instrumentation makes each scenario take many minutes;
// nightly.yml's "Embedded Dolt batch-apply suite (non-race)" step runs it,
// and the server backend's full suite (non-race) runs the same scenarios on
// every PR that takes that tier.
func TestCreateBatchFastPathsMatchPerRowLarge_Embedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	if raceEnabled {
		t.Skip("458-issue equivalence scenarios skipped under -race; nightly.yml's non-race embedded step runs them")
	}
	createbatchequiv.RunLarge(t, openEquivalenceDB)
}

func openEquivalenceDB(t *testing.T) *sql.DB {
	t.Helper()
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
}
