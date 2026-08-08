package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

func TestClaimerClaimsAnUnassignedOpenIssueAndAnswersTheBareRow(t *testing.T) {
	fixture, ctx, cleanup := newDoltClaimerFixture(t, "clwin")
	defer cleanup()
	conformance.RunClaimerClaimsAnUnassignedOpenIssueAndAnswersTheBareRow(t, ctx, fixture)
}

func TestClaimerTakesAnOpenIssueTheActorAlreadyHolds(t *testing.T) {
	fixture, ctx, cleanup := newDoltClaimerFixture(t, "clmine")
	defer cleanup()
	conformance.RunClaimerTakesAnOpenIssueTheActorAlreadyHolds(t, ctx, fixture)
}

func TestClaimerReclaimsItsOwnInProgressIssueWithoutWriting(t *testing.T) {
	fixture, ctx, cleanup := newDoltClaimerFixture(t, "clidem")
	defer cleanup()
	conformance.RunClaimerReclaimsItsOwnInProgressIssueWithoutWriting(t, ctx, fixture)
}

func TestClaimerAcceptsAConfiguredActiveStatusAndRefusesAConfiguredWipOne(t *testing.T) {
	fixture, ctx, cleanup := newDoltClaimerFixture(t, "clcustom")
	defer cleanup()
	conformance.RunClaimerAcceptsAConfiguredActiveStatusAndRefusesAConfiguredWipOne(t, ctx, fixture)
}

func TestClaimerTakesAPoolAssignedIssueButRefusesAForeignHolder(t *testing.T) {
	fixture, ctx, cleanup := newDoltClaimerFixture(t, "clpool")
	defer cleanup()
	conformance.RunClaimerTakesAPoolAssignedIssueButRefusesAForeignHolder(t, ctx, fixture)
}

func TestClaimerRefusesABuiltInIneligibleStatusWithTheStateThatRefusedIt(t *testing.T) {
	fixture, ctx, cleanup := newDoltClaimerFixture(t, "clstatus")
	defer cleanup()
	conformance.RunClaimerRefusesABuiltInIneligibleStatusWithTheStateThatRefusedIt(t, ctx, fixture)
}

func TestClaimerGrantsALiveLeaseOnTheRowItWonAndLeavesARefusedOneAlone(t *testing.T) {
	fixture, ctx, cleanup := newDoltClaimerFixture(t, "cllease")
	defer cleanup()
	conformance.RunClaimerGrantsALiveLeaseOnTheRowItWonAndLeavesARefusedOneAlone(t, ctx, fixture)
}

func TestClaimerStampsStartedAtOnceAcrossTheTwoWritesItChoosesBetween(t *testing.T) {
	fixture, ctx, cleanup := newDoltClaimerFixture(t, "clstart")
	defer cleanup()
	conformance.RunClaimerStampsStartedAtOnceAcrossTheTwoWritesItChoosesBetween(t, ctx, fixture)
}

func TestClaimerRefusesAWispIDAsNotFound(t *testing.T) {
	fixture, ctx, cleanup := newDoltClaimerFixture(t, "clwisp")
	defer cleanup()
	conformance.RunClaimerRefusesAWispIDAsNotFound(t, ctx, fixture)
}

func TestClaimerRefusesIncompleteRequestsAndAnAbsentIDWithoutTouchingState(t *testing.T) {
	fixture, ctx, cleanup := newDoltClaimerFixture(t, "clreject")
	defer cleanup()
	conformance.RunClaimerRefusesIncompleteRequestsAndAnAbsentIDWithoutTouchingState(t, ctx, fixture)
}

// newDoltClaimerFixture composes the frozen role kit with this backend's claim
// accessor. A store per case is affordable here — setupTestStore hands out a
// copy-on-write branch of the shared package server — which is why this wiring
// keeps one test function per case while the unit-of-work one cannot. It also
// keeps the two config-writing cases from seeing each other's status.custom and
// claim.pools, which are workspace-global keys.
func newDoltClaimerFixture(t *testing.T, prefix string) (conformance.ClaimerFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	claimer, err := store.IssueClaimer()
	if err != nil {
		stop()
		t.Fatalf("IssueClaimer(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.ClaimerFixture{
		IssuePrefix:  kit.IssuePrefix,
		Claimer:      claimer,
		CreateIssue:  kit.CreateIssue,
		CreateWisp:   kit.CreateWisp,
		SetConfig:    kit.SetConfig,
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}, ctx, stop
}

func TestClaimerRefusalMessagesCarryTheirFragments(t *testing.T) {
	fixture, ctx, cleanup := newDoltClaimerFixture(t, "clfrag")
	defer cleanup()
	conformance.RunClaimerRefusalMessagesCarryTheirFragments(t, ctx, fixture)
}
