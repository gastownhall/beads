//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestReadyCounterContract runs the ReadyCounter contract against the embedded
// store, which hands back the SAME body the server-backed store does
// (internal/workapi/storereadycounter) and differs only in the engine
// underneath. That is what this wiring catches; it is not an independent vote
// on the body.
//
// One environment for the whole suite: booting an embedded engine per case
// would dominate the runtime, the ids and labels are prefix-namespaced and
// every request is scoped to them, and the history delta needs the subtests
// sequential anyway.
func TestReadyCounterContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "rdc")
	ctx := t.Context()
	fixture := newEmbeddedReadyCounterFixture(t, te, "rdc")

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

func newEmbeddedReadyCounterFixture(t *testing.T, te *testEnv, prefix string) conformance.ReadyCounterFixture {
	t.Helper()
	counter, err := te.store.ReadyCounter()
	if err != nil {
		t.Fatalf("ReadyCounter(): %v", err)
	}
	reader, err := te.store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.ReadyCounterFixture{
		IssuePrefix:   kit.IssuePrefix,
		ReadyCounter:  counter,
		Reader:        reader,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		CountHistory:  kit.CountHistory,
	}
}
