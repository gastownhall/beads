//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/createbatchequiv"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
)

// createBatchEquivalenceParts spreads createbatchequiv.Run's scenarios over
// the two TestCreateBatchFastPathsMatchPerRow*_Embedded tests below: under
// -race they took ~110s as one test, a whole CI shard on their own.
// TestCreateBatchEquivalencePartsCoverEveryScenario keeps the split exact.
var createBatchEquivalenceParts = [][]string{
	{"small", "depadd"},
	{"apply", "waitsfor"},
}

// TestCreateBatchFastPathsMatchPerRowSmallDepAdd_Embedded and
// TestCreateBatchFastPathsMatchPerRowApplyWaitsFor_Embedded run the
// batch-create equivalence scenarios (internal/storage/createbatchequiv) on
// the embedded engine: the same import-shaped batch through the fast and
// per-row bodies must store the same rows.
func TestCreateBatchFastPathsMatchPerRowSmallDepAdd_Embedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	createbatchequiv.RunNamed(t, openEquivalenceDB, createBatchEquivalenceParts[0]...)
}

func TestCreateBatchFastPathsMatchPerRowApplyWaitsFor_Embedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	createbatchequiv.RunNamed(t, openEquivalenceDB, createBatchEquivalenceParts[1]...)
}

// TestCreateBatchEquivalencePartsCoverEveryScenario fails unless the parts
// above name every createbatchequiv.Run scenario exactly once, so a scenario
// added there cannot silently stop running on this backend.
func TestCreateBatchEquivalencePartsCoverEveryScenario(t *testing.T) {
	var got []string
	for _, part := range createBatchEquivalenceParts {
		got = append(got, part...)
	}
	want := createbatchequiv.ScenarioNames()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("createBatchEquivalenceParts cover %v, want each of %v exactly once", got, want)
	}
}

// TestCreateBatchFastPathsMatchPerRowLarge_Embedded runs the 458-issue
// equivalence scenarios. It is skipped under -race, where the in-process
// engine's own instrumentation makes each scenario take many minutes;
// //internal/storage/embeddeddolt:embeddeddolt_batch_apply_nonrace_test (non-race) runs it,
// and the server backend's full suite (non-race) runs the same scenarios on
// every PR that takes that tier.
func TestCreateBatchFastPathsMatchPerRowLarge_Embedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	if raceEnabled {
		t.Skip("458-issue equivalence scenarios skipped under -race; embeddeddolt_batch_apply_nonrace_test runs them")
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
