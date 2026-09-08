package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestReadyClaimerContract runs the whole ReadyClaimer contract against ONE
// provider, as subtests, and that shape is the point rather than a detail.
//
// Every newTestUOWProvider boots a real Dolt sql-server, so a wiring with one
// test function per case pays that boot per case and a five-role suite pays it
// forty times; the conformance fixtures were designed to be shared precisely so
// it does not have to. IssuePrefix namespaces the ids each case seeds, and the
// contract additionally scopes every case's ready question to a label only that
// case uses, so cases cannot claim each other's rows out of the one database.
//
// NO t.Parallel, ANYWHERE IN HERE. This backend has no per-test copy-on-write
// branch, so dolt_log and the event tables are database-global; a parallel
// subtest would corrupt another subtest's history-delta arithmetic. The two
// store backends get isolation for free and their wirings are one function per
// case; this one buys it with sequencing.
func TestReadyClaimerContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWReadyClaimerFixture(t, ctx, "rc")

	for _, test := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.ReadyClaimerFixture)
	}{
		{name: "RejectsLimitOffsetBriefAndEmptyActor", run: conformance.RunReadyClaimerRejectsLimitOffsetBriefAndEmptyActor},
		{name: "EmptyFrontIsNormal", run: conformance.RunReadyClaimerEmptyFrontIsNormal},
		{name: "ClaimsTheFrontRowAndReturnsThePostClaimState", run: conformance.RunReadyClaimerClaimsTheFrontRowAndReturnsThePostClaimState},
		{name: "ClaimsAnEphemeralRowTheFilterAdmits", run: conformance.RunReadyClaimerClaimsAnEphemeralRowTheFilterAdmits},
		{name: "LeavesEphemeralRowsOutOfTheDefaultReadySet", run: conformance.RunReadyClaimerLeavesEphemeralRowsOutOfTheDefaultReadySet},
		{name: "LeasesADurableWinButNotAnEphemeralOne", run: conformance.RunReadyClaimerLeasesADurableWinButNotAnEphemeralOne},
		{name: "FencesTheClaimByEveryLabelSetAndTheParentItWasGiven", run: conformance.RunReadyClaimerFencesTheClaimByEveryLabelSetAndTheParentItWasGiven},
		{name: "HydratesOnlyItsBlocksEdgesIntoTheCardinalities", run: conformance.RunReadyClaimerHydratesOnlyItsBlocksEdgesIntoTheCardinalities},
		{name: "AnswersTheQuestionReaderReadyLists", run: conformance.RunReadyClaimerAnswersTheQuestionReaderReadyLists},
		{name: "SkipsIneligibleFrontRows", run: conformance.RunReadyClaimerSkipsIneligibleFrontRows},
		{name: "RecordsOneHistoryEntryForAWin", run: conformance.RunReadyClaimerRecordsOneHistoryEntryForAWin},
		{name: "DoesNotMutateTheCallerRequest", run: conformance.RunReadyClaimerDoesNotMutateTheCallerRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.run(t, ctx, fixture)
		})
	}
}

// newUOWReadyClaimerFixture composes the frozen role kit with this backend's
// two accessors over one provider.
func newUOWReadyClaimerFixture(t *testing.T, ctx context.Context, prefix string) conformance.ReadyClaimerFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	claimer, err := NewReadyClaimer(provider)
	if err != nil {
		t.Fatalf("NewReadyClaimer: %v", err)
	}
	reader, err := NewIssueReader(provider)
	if err != nil {
		t.Fatalf("NewIssueReader: %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.ReadyClaimerFixture{
		IssuePrefix:   kit.IssuePrefix,
		Claimer:       claimer,
		Reader:        reader,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		QueryScalar:   kit.QueryScalar,
		CountHistory:  kit.CountHistory,
	}
}
