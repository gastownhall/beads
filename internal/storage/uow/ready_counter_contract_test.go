package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestReadyCounterContract runs the ReadyCounter contract against the
// unit-of-work provider — the one implementation that answers by counting the
// unbounded page rather than with an indexed COUNT(*). It is the SECOND of two
// votes, not the third: the two store backends share the other body.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so a parallel subtest would corrupt another's history
// delta.
func TestReadyCounterContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWReadyCounterFixture(t, ctx, "rdc")

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

func newUOWReadyCounterFixture(t *testing.T, ctx context.Context, prefix string) conformance.ReadyCounterFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessors, not NewReadyCounter/NewIssueReader: a
	// provider that stopped offering either role is the regression a constructor
	// call would hide.
	source, ok := provider.(ReadyCounterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the ReadyCounter accessor", provider)
	}
	counter, err := source.ReadyCounter()
	if err != nil {
		t.Fatalf("ReadyCounter(): %v", err)
	}
	readerSource, ok := provider.(IssueReaderSource)
	if !ok {
		t.Fatalf("provider %T does not offer the IssueReader accessor", provider)
	}
	reader, err := readerSource.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.ReadyCounterFixture{
		IssuePrefix:   kit.IssuePrefix,
		ReadyCounter:  counter,
		Reader:        reader,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
	}
}
