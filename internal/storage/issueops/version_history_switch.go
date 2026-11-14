package issueops

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/steveyegge/beads/internal/storage"
)

// VersionedHistorySwitch is one plumbing's versioned-history activation: the
// value behind its storage.VersionedHistoryConfigurer and
// storage.VersionedHistoryDeferrer methods. The dolt store, the embedded store
// and the unit-of-work provider each hold one, so the rule for when a deferred
// read happens is written once.
//
// It is on, off, or PENDING: handed a storage.VersionedHistoryResolver that
// has not answered yet. Enabled reports the answer so far, false while
// pending. ResolveIn is what a write transaction asks: it answers a pending
// switch through that transaction, once, before the transaction's body runs,
// or fails that transaction when the read does.
//
// THE ANSWER ONLY EVER MOVES FROM OFF TO ON, outside tests (Set(false) is the
// conformance fixtures' cleanup). That is what keeps the deferred read inside
// the invariant versioncontrolops.DirtyTableTracker.MarkVersionedHistoryDirty
// documents: a flip landing between a transaction's scope and its staging is
// harmful only from on to off, which minted rows and then declined to stage
// them. Off to on stages a table that may be clean, which costs one DOLT_ADD
// that stages nothing.
//
// The zero value is off, with nothing pending. A switch must not be copied
// after first use.
type VersionedHistorySwitch struct {
	enabled atomic.Bool
	// hasPending is the lock-free fast path: once the switch has answered,
	// ResolveIn is one atomic load, the cost of the atomic.Bool it replaces.
	hasPending atomic.Bool

	mu      sync.Mutex
	pending storage.VersionedHistoryResolver // guarded by mu
}

// Set answers the switch outright and drops any pending read. It is
// SetVersionedHistoryEnabled.
func (s *VersionedHistorySwitch) Set(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Publish the answer before clearing the pending flag: a writer on the
	// lock-free path that sees hasPending false must also see this answer.
	s.enabled.Store(enabled)
	s.pending = nil
	s.hasPending.Store(false)
}

// Defer leaves the switch for resolve to answer at the next write. It is
// DeferVersionedHistory. An enabled switch is left alone: a deferred read can
// only turn recording on, the same rule Apply follows. A switch that already
// answered off is re-armed, so a repeated ActivateStore on one instance reads
// the row again, as it did when activation read the row eagerly.
func (s *VersionedHistorySwitch) Defer(resolve storage.VersionedHistoryResolver) {
	if resolve == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enabled.Load() {
		return
	}
	s.pending = resolve
	s.hasPending.Store(true)
}

// Enabled reports the answer so far without asking for one: false while the
// read is pending. It is for the paths that cannot mint (a read transaction)
// or that run after a ResolveIn in the same operation (the staging that
// follows a write), where the two cannot disagree.
func (s *VersionedHistorySwitch) Enabled() bool {
	return s.enabled.Load()
}

// ResolveIn answers the switch for a write about to run on q, reading the row
// through q if the read is still pending. Concurrent writers wait for the
// first one's read rather than each making their own.
//
// A read that fails is not an answer: ResolveIn returns its error and leaves
// the switch pending, and the caller fails its write before the body runs, so
// the write's retry, or the next write, reads the row again. Running the body
// scoped off instead fails open: a create commits with no participation stamp,
// which is the legacy mark, and once a later write turns recording on the
// write fence skips that issue for good (RecordVersionForCreateInTx).
// Settling off would let one interrupted request switch recording off for the
// rest of a long-lived process (bd serve) while the row says on.
//
// The mutex is held across the first resolver's round trip and does not
// watch ctx: other first writers wait on it while holding their own open
// transaction or pinned connection, and their deadlines cannot cancel the
// wait. That is accepted: it happens only for the first resolution per
// instance, the wait is bounded by that read's own ctx and driver timeouts,
// and it cannot deadlock because every caller reads through a transaction or
// connection it already holds.
func (s *VersionedHistorySwitch) ResolveIn(ctx context.Context, q DBTX) (bool, error) {
	if !s.hasPending.Load() {
		return s.enabled.Load(), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return s.enabled.Load(), nil
	}
	on, err := s.pending(ctx, func(ctx context.Context, key string) (string, error) {
		return GetConfigInTx(ctx, q, key)
	})
	if err != nil {
		return false, err
	}
	// Publish the answer before clearing hasPending. In the other order a
	// writer arriving between the two stores takes the lock-free path, reads
	// hasPending false and then the old enabled false, and is scoped off
	// while the row says on: a write that mints no version.
	if on {
		s.enabled.Store(true)
	}
	s.pending = nil
	s.hasPending.Store(false)
	return s.enabled.Load(), nil
}
