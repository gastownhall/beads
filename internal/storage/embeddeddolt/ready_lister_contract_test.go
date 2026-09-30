//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestReadyListerContract runs the ReadyLister contract against the embedded
// store, which hands back the SAME body the server-backed store does
// (internal/workapi/storereadylister) and differs only in the engine
// underneath. That is what this wiring catches; it is not an independent vote
// on the body.
func TestReadyListerContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "rdl")
	ctx := t.Context()
	fixture := newEmbeddedReadyListerFixture(t, te, "rdl")

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

func newEmbeddedReadyListerFixture(t *testing.T, te *testEnv, prefix string) conformance.ReadyListerFixture {
	t.Helper()
	lister, err := te.store.ReadyLister()
	if err != nil {
		t.Fatalf("ReadyLister(): %v", err)
	}
	reader, err := te.store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	counter, err := te.store.ReadyCounter()
	if err != nil {
		t.Fatalf("ReadyCounter(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.ReadyListerFixture{
		IssuePrefix:   kit.IssuePrefix,
		ReadyLister:   lister,
		Reader:        reader,
		ReadyCounter:  counter,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		CountHistory:  kit.CountHistory,
	}
}
