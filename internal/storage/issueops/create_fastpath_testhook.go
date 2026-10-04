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
// fast-vs-per-row equivalence tests (internal/storage/createbatchequiv); a
// caller must not run concurrently with another caller of it.
func DisableCreateFastPathsForTest() (restore func()) {
	createFastPathsDisabled.Store(true)
	return func() { createFastPathsDisabled.Store(false) }
}
