package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestReadyCounterContract runs the ReadyCounter contract against the
// server-backed store, which reaches the cheap indexed COUNT(*) path
// (internal/workapi/storereadycounter over CountReadyWorkInTx).
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch. WritesNothing takes a before/after history
// delta, which is only meaningful while the subtests run sequentially:
// setupTestStore already marks the PARENT parallel, and no subtest here calls
// t.Parallel.
func TestReadyCounterContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyCounterFixture(t, "rdc")
	defer cleanup()

	t.Run("EqualsTheUnboundedPage", func(t *testing.T) {
		conformance.RunReadyCounterEqualsTheUnboundedPage(t, ctx, fixture)
	})
	t.Run("RejectsLimitAndOffset", func(t *testing.T) {
		conformance.RunReadyCounterRejectsLimitAndOffset(t, ctx, fixture)
	})
	t.Run("CountsTheBlockerAwareSet", func(t *testing.T) {
		conformance.RunReadyCounterCountsTheBlockerAwareSet(t, ctx, fixture)
	})
	t.Run("EphemeralGateMatchesTheListing", func(t *testing.T) {
		conformance.RunReadyCounterEphemeralGateMatchesTheListing(t, ctx, fixture)
	})
	t.Run("CountsOnlyTheOpenRowsItsListingLists", func(t *testing.T) {
		conformance.RunReadyCounterCountsOnlyTheOpenRowsItsListingLists(t, ctx, fixture)
	})
	t.Run("EmptyFrontIsZeroAndNil", func(t *testing.T) {
		conformance.RunReadyCounterEmptyFrontIsZeroAndNil(t, ctx, fixture)
	})
	t.Run("WritesNothing", func(t *testing.T) {
		conformance.RunReadyCounterWritesNothing(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunReadyCounterDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
}

// newDoltReadyCounterFixture composes the frozen role kit with this backend's
// two accessors — the surface under test and the reader the identity case
// compares it against.
func newDoltReadyCounterFixture(t *testing.T, prefix string) (conformance.ReadyCounterFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	counter, err := store.ReadyCounter()
	if err != nil {
		stop()
		t.Fatalf("ReadyCounter(): %v", err)
	}
	reader, err := store.IssueReader()
	if err != nil {
		stop()
		t.Fatalf("IssueReader(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.ReadyCounterFixture{
		IssuePrefix:   kit.IssuePrefix,
		ReadyCounter:  counter,
		Reader:        reader,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
	}, ctx, stop
}
