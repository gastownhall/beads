package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

func TestReadyClaimerRejectsLimitOffsetBriefAndEmptyActor(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyClaimerFixture(t, "rcrej")
	defer cleanup()
	conformance.RunReadyClaimerRejectsLimitOffsetBriefAndEmptyActor(t, ctx, fixture)
}

func TestReadyClaimerEmptyFrontIsNormal(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyClaimerFixture(t, "rcempty")
	defer cleanup()
	conformance.RunReadyClaimerEmptyFrontIsNormal(t, ctx, fixture)
}

func TestReadyClaimerClaimsTheFrontRowAndReturnsThePostClaimState(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyClaimerFixture(t, "rcwin")
	defer cleanup()
	conformance.RunReadyClaimerClaimsTheFrontRowAndReturnsThePostClaimState(t, ctx, fixture)
}

func TestReadyClaimerClaimsAnEphemeralRowTheFilterAdmits(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyClaimerFixture(t, "rcwisp")
	defer cleanup()
	conformance.RunReadyClaimerClaimsAnEphemeralRowTheFilterAdmits(t, ctx, fixture)
}

func TestReadyClaimerLeavesEphemeralRowsOutOfTheDefaultReadySet(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyClaimerFixture(t, "rcdefault")
	defer cleanup()
	conformance.RunReadyClaimerLeavesEphemeralRowsOutOfTheDefaultReadySet(t, ctx, fixture)
}

func TestReadyClaimerLeasesADurableWinButNotAnEphemeralOne(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyClaimerFixture(t, "rclease")
	defer cleanup()
	conformance.RunReadyClaimerLeasesADurableWinButNotAnEphemeralOne(t, ctx, fixture)
}

func TestReadyClaimerFencesTheClaimByEveryLabelSetAndTheParentItWasGiven(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyClaimerFixture(t, "rcfence")
	defer cleanup()
	conformance.RunReadyClaimerFencesTheClaimByEveryLabelSetAndTheParentItWasGiven(t, ctx, fixture)
}

func TestReadyClaimerHydratesOnlyItsBlocksEdgesIntoTheCardinalities(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyClaimerFixture(t, "rccount")
	defer cleanup()
	conformance.RunReadyClaimerHydratesOnlyItsBlocksEdgesIntoTheCardinalities(t, ctx, fixture)
}

func TestReadyClaimerAnswersTheQuestionReaderReadyLists(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyClaimerFixture(t, "rcagree")
	defer cleanup()
	conformance.RunReadyClaimerAnswersTheQuestionReaderReadyLists(t, ctx, fixture)
}

func TestReadyClaimerSkipsIneligibleFrontRows(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyClaimerFixture(t, "rcskip")
	defer cleanup()
	conformance.RunReadyClaimerSkipsIneligibleFrontRows(t, ctx, fixture)
}

func TestReadyClaimerRecordsOneHistoryEntryForAWin(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyClaimerFixture(t, "rchist")
	defer cleanup()
	conformance.RunReadyClaimerRecordsOneHistoryEntryForAWin(t, ctx, fixture)
}

func TestReadyClaimerDoesNotMutateTheCallerRequest(t *testing.T) {
	fixture, ctx, cleanup := newDoltReadyClaimerFixture(t, "rcsnap")
	defer cleanup()
	conformance.RunReadyClaimerDoesNotMutateTheCallerRequest(t, ctx, fixture)
}

// newDoltReadyClaimerFixture composes the frozen role kit with this backend's
// two accessors. A store per case is affordable here — setupTestStore hands out
// a copy-on-write branch of the shared package server — which is why this
// wiring keeps one test function per case while the unit-of-work one cannot.
func newDoltReadyClaimerFixture(t *testing.T, prefix string) (conformance.ReadyClaimerFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	claimer, err := store.ReadyClaimer()
	if err != nil {
		stop()
		t.Fatalf("ReadyClaimer(): %v", err)
	}
	reader, err := store.IssueReader()
	if err != nil {
		stop()
		t.Fatalf("IssueReader(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.ReadyClaimerFixture{
		IssuePrefix:   kit.IssuePrefix,
		Claimer:       claimer,
		Reader:        reader,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
	}, ctx, stop
}
