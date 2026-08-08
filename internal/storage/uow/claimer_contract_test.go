package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestClaimerContract runs the whole Claimer contract against ONE provider, as
// subtests, and that shape is the point rather than a detail.
//
// Every newTestUOWProvider boots a real Dolt sql-server, so a wiring with one
// test function per case pays that boot per case; the conformance fixtures were
// designed to be shared precisely so it does not have to. IssuePrefix
// namespaces the ids each case seeds, so cases cannot claim each other's rows
// out of the one database.
//
// THE TWO CONFIG-WRITING CASES SHARE THAT DATABASE, which the two store wirings
// avoid by taking a fresh workspace per case. status.custom and claim.pools are
// workspace-global keys, so this ordering is load-bearing in one direction
// only: both cases namespace what they install with IssuePrefix — the custom
// statuses and the pool alias are names no other case's row carries — so
// neither leaks eligibility into a neighbour. A case that installed a BARE
// alias or a bare status name would.
//
// NO t.Parallel, ANYWHERE IN HERE. This backend has no per-test copy-on-write
// branch, so dolt_log is database-global; a parallel subtest would corrupt
// another subtest's history-delta arithmetic. The two store backends get
// isolation for free and their wirings are one function per case; this one buys
// it with sequencing.
func TestClaimerContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWClaimerFixture(t, ctx, "cl")

	for _, test := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.ClaimerFixture)
	}{
		{name: "ClaimsAnUnassignedOpenIssueAndAnswersTheBareRow", run: conformance.RunClaimerClaimsAnUnassignedOpenIssueAndAnswersTheBareRow},
		{name: "TakesAnOpenIssueTheActorAlreadyHolds", run: conformance.RunClaimerTakesAnOpenIssueTheActorAlreadyHolds},
		{name: "ReclaimsItsOwnInProgressIssueWithoutWriting", run: conformance.RunClaimerReclaimsItsOwnInProgressIssueWithoutWriting},
		{name: "AcceptsAConfiguredActiveStatusAndRefusesAConfiguredWipOne", run: conformance.RunClaimerAcceptsAConfiguredActiveStatusAndRefusesAConfiguredWipOne},
		{name: "TakesAPoolAssignedIssueButRefusesAForeignHolder", run: conformance.RunClaimerTakesAPoolAssignedIssueButRefusesAForeignHolder},
		{name: "RefusesABuiltInIneligibleStatusWithTheStateThatRefusedIt", run: conformance.RunClaimerRefusesABuiltInIneligibleStatusWithTheStateThatRefusedIt},
		{name: "RefusesAWispIDAsNotFound", run: conformance.RunClaimerRefusesAWispIDAsNotFound},
		{name: "RefusesIncompleteRequestsAndAnAbsentIDWithoutTouchingState", run: conformance.RunClaimerRefusesIncompleteRequestsAndAnAbsentIDWithoutTouchingState},
		{name: "RefusalMessagesCarryTheirFragments", run: conformance.RunClaimerRefusalMessagesCarryTheirFragments},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.run(t, ctx, fixture)
		})
	}
}

// newUOWClaimerFixture composes the frozen role kit with this backend's claim
// accessor over one provider.
func newUOWClaimerFixture(t *testing.T, ctx context.Context, prefix string) conformance.ClaimerFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	claimer, err := NewIssueClaimer(provider)
	if err != nil {
		t.Fatalf("NewIssueClaimer: %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.ClaimerFixture{
		IssuePrefix:  kit.IssuePrefix,
		Claimer:      claimer,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		SetConfig:    kit.SetConfig,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
}
