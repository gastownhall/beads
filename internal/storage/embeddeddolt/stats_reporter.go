//go:build cgo

package embeddeddolt

import (
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/workapi/storestats"
	"github.com/steveyegge/beads/issueops"
)

// StatsReporter returns the guarded summary-statistics surface for this store.
func (s *EmbeddedDoltStore) StatsReporter() (issueops.StatsReporter, error) {
	return newStatsReporter(s)
}

// newStatsReporter returns guarded summary statistics backed by store.
//
// The implementation is the shared one: the two Dolt-backed stores differ below
// storage.DoltStorage, not above it, so a second copy here would be a copy of
// nothing but the constructor. The statistics QUERIES underneath are this
// package's own (statistics.go), which is why this backend is a real vote in
// the conformance run rather than an engine check on the other's body.
func newStatsReporter(store *EmbeddedDoltStore) (issueops.StatsReporter, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "newStatsReporter", Backend: "nil"}
	}
	return storestats.New(store)
}
