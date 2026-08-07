package dolt

import (
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/workapi/storestats"
	"github.com/steveyegge/beads/issueops"
)

// StatsReporter returns the guarded summary-statistics surface for this store.
func (s *DoltStore) StatsReporter() (issueops.StatsReporter, error) {
	return newStatsReporter(s)
}

// newStatsReporter returns guarded summary statistics backed by store.
//
// The implementation is the shared one, for the reason newCounter gives: the
// two Dolt-backed stores differ below storage.DoltStorage, not above it, so a
// second copy here would be a copy of nothing but the constructor. The
// statistics QUERIES underneath do differ — each store writes its own
// GetStatistics — which is what makes this role's conformance run three
// genuine votes instead of two.
func newStatsReporter(store *DoltStore) (issueops.StatsReporter, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "newStatsReporter", Backend: "nil"}
	}
	return storestats.New(store)
}
