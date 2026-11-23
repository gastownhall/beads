//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/batchbench"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
)

// TestLargeGraphCreateTiming_Embedded runs the large-graph create benchmark
// (internal/storage/batchbench) on the embedded engine. Opt-in via
// batchbench.EnvVar; it logs and asserts nothing.
func TestLargeGraphCreateTiming_Embedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	issues := batchbench.Issues(t)
	fixture := newPristineEmbeddedDoltFixture(t, "lgbench")
	t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })
	counts := &sqlcount.Counts{}
	db, cleanup, err := openCountedConn(t.Context(), fixture.dataDir, fixture.database, counts)
	if err != nil {
		t.Fatalf("openCountedConn: %v", err)
	}
	t.Cleanup(cleanup)
	batchbench.Run(t, db, counts, "lgbench", issues)
}
