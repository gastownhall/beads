package dolt

import (
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/searchcountsequiv"
)

// TestSearchCountsIDsFirstMatchesPredicateForm_Dolt runs the counts-search
// equivalence scenarios (internal/storage/searchcountsequiv) on the Dolt server
// backend: a bounded page answered ids-first, and the by-IDs form on its own,
// must return the predicate form's rows (#7635).
func TestSearchCountsIDsFirstMatchesPredicateForm_Dolt(t *testing.T) {
	searchcountsequiv.Run(t, func(t *testing.T) *sql.DB {
		t.Helper()
		store, cleanup := setupConcurrentTestStore(t)
		t.Cleanup(cleanup)
		return store.db
	})
}
