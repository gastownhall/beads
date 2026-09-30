package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestReadyListerContract runs the ReadyLister contract against the
// unit-of-work provider — the SECOND of two votes: it pages through the domain
// seam and counts the unbounded page only when rows were hidden, where the two
// store backends share the single-pass body.
//
// One provider for the whole suite and NO t.Parallel, for the reasons
// TestReadyCounterContract gives.
func TestReadyListerContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWReadyListerFixture(t, ctx, "rdl")

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

func newUOWReadyListerFixture(t *testing.T, ctx context.Context, prefix string) conformance.ReadyListerFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessors, not the constructors: a provider that
	// stopped offering a role is the regression a constructor call would hide.
	source, ok := provider.(ReadyListerSource)
	if !ok {
		t.Fatalf("provider %T does not offer the ReadyLister accessor", provider)
	}
	lister, err := source.ReadyLister()
	if err != nil {
		t.Fatalf("ReadyLister(): %v", err)
	}
	readerSource, ok := provider.(IssueReaderSource)
	if !ok {
		t.Fatalf("provider %T does not offer the IssueReader accessor", provider)
	}
	reader, err := readerSource.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	counterSource, ok := provider.(ReadyCounterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the ReadyCounter accessor", provider)
	}
	counter, err := counterSource.ReadyCounter()
	if err != nil {
		t.Fatalf("ReadyCounter(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
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
