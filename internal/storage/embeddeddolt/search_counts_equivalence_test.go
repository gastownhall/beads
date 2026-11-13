//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/searchcountsequiv"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
)

// TestSearchCountsIDsFirstMatchesPredicateForm_Embedded runs the counts-search
// equivalence scenarios (internal/storage/searchcountsequiv) on the embedded
// engine: a bounded page answered ids-first, and the by-IDs form on its own,
// must return the predicate form's rows (#7635).
func TestSearchCountsIDsFirstMatchesPredicateForm_Embedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	searchcountsequiv.Run(t, func(t *testing.T) *sql.DB {
		t.Helper()
		fixture := newPristineEmbeddedDoltFixture(t, "eq")
		t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })
		db, cleanup, err := openCountedConn(t.Context(), fixture.dataDir, fixture.database, &sqlcount.Counts{})
		if err != nil {
			t.Fatalf("openCountedConn: %v", err)
		}
		t.Cleanup(cleanup)
		return db
	})
}
