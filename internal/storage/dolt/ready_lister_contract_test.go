package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestReadyListerContract runs the ReadyLister contract against the
// server-backed store, which reaches the single-pass body
// (internal/workapi/storereadylister over GetReadyWorkWithCountsAndTotalInTx).
//
// One store and one copy-on-write branch for the whole suite; WritesNothing
// takes a history delta, so no subtest calls t.Parallel.
func TestReadyListerContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyListerFixture(t, "rdl")
	defer cleanup()

	t.Run("AgreesWithReadyAndCountReady", func(t *testing.T) {
		conformance.RunReadyListerAgreesWithReadyAndCountReady(t, ctx, fixture)
	})
	t.Run("TotalCountsTheWispPlaneItAdmits", func(t *testing.T) {
		conformance.RunReadyListerTotalCountsTheWispPlaneItAdmits(t, ctx, fixture)
	})
	t.Run("HonorsExcludeIDs", func(t *testing.T) {
		conformance.RunReadyListerHonorsExcludeIDs(t, ctx, fixture)
	})
	t.Run("RefusesPastTheRowCap", func(t *testing.T) {
		conformance.RunReadyListerRefusesPastTheRowCap(t, ctx, fixture)
	})
	t.Run("RejectsWhatReadyRejects", func(t *testing.T) {
		conformance.RunReadyListerRejectsWhatReadyRejects(t, ctx, fixture)
	})
	t.Run("EmptyFrontIsEmptyAndZero", func(t *testing.T) {
		conformance.RunReadyListerEmptyFrontIsEmptyAndZero(t, ctx, fixture)
	})
	t.Run("WritesNothing", func(t *testing.T) {
		conformance.RunReadyListerWritesNothing(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunReadyListerDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
}

// newDoltReadyListerFixture composes the frozen role kit with this backend's
// three accessors: the surface under test and the two it is an identity with.
func newDoltReadyListerFixture(t *testing.T, prefix string) (conformance.ReadyListerFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	lister, err := store.ReadyLister()
	if err != nil {
		stop()
		t.Fatalf("ReadyLister(): %v", err)
	}
	reader, err := store.IssueReader()
	if err != nil {
		stop()
		t.Fatalf("IssueReader(): %v", err)
	}
	counter, err := store.ReadyCounter()
	if err != nil {
		stop()
		t.Fatalf("ReadyCounter(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.ReadyListerFixture{
		IssuePrefix:   kit.IssuePrefix,
		ReadyLister:   lister,
		Reader:        reader,
		ReadyCounter:  counter,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		CountHistory:  kit.CountHistory,
	}, ctx, stop
}
