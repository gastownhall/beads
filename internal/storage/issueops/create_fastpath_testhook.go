package issueops

import "sync/atomic"

// createFastPathsDisabled turns off the batch-create fast paths — the
// createBatchCache, the dependency pass's batch lookups and in-memory graph,
// and the blocked-state recompute's no-edge shortcut — so a test can run the
// same batch through both the fast and the per-row bodies on a real engine
// and compare the stored outcome. Production never sets it.
var createFastPathsDisabled atomic.Bool

// DisableCreateFastPathsForTest switches the batch-create fast paths off for
// the whole process until the returned restore runs. It exists only for the
// fast-vs-per-row equivalence tests (internal/storage/createbatchequiv).
//
// It is process-global, and it also turns off the blocked-state no-edge
// shortcut for every recompute in the process. A test that calls it must not
// use t.Parallel (a sequential top-level test never overlaps parallel ones),
// and two callers must not overlap: a second call while one is active panics.
func DisableCreateFastPathsForTest() (restore func()) {
	if !createFastPathsDisabled.CompareAndSwap(false, true) {
		panic("issueops.DisableCreateFastPathsForTest: already disabled by an overlapping caller")
	}
	return func() { createFastPathsDisabled.Store(false) }
}
