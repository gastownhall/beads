//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

func TestEmbeddedClaimerClaimsAnUnassignedOpenIssueAndAnswersTheBareRow(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunClaimerClaimsAnUnassignedOpenIssueAndAnswersTheBareRow(t, ctx, newEmbeddedClaimerFixture(t, "clwin"))
}

func TestEmbeddedClaimerTakesAnOpenIssueTheActorAlreadyHolds(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunClaimerTakesAnOpenIssueTheActorAlreadyHolds(t, ctx, newEmbeddedClaimerFixture(t, "clmine"))
}

func TestEmbeddedClaimerReclaimsItsOwnInProgressIssueWithoutWriting(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunClaimerReclaimsItsOwnInProgressIssueWithoutWriting(t, ctx, newEmbeddedClaimerFixture(t, "clidem"))
}

func TestEmbeddedClaimerReclaimsAcrossSpellingWithoutWriting(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunClaimerReclaimsAcrossSpellingWithoutWriting(t, ctx, newEmbeddedClaimerFixture(t, "clspell"))
}

func TestEmbeddedClaimerAcceptsAConfiguredActiveStatusAndRefusesAConfiguredWipOne(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunClaimerAcceptsAConfiguredActiveStatusAndRefusesAConfiguredWipOne(t, ctx, newEmbeddedClaimerFixture(t, "clcustom"))
}

func TestEmbeddedClaimerTakesAPoolAssignedIssueButRefusesAForeignHolder(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunClaimerTakesAPoolAssignedIssueButRefusesAForeignHolder(t, ctx, newEmbeddedClaimerFixture(t, "clpool"))
}

func TestEmbeddedClaimerRefusesABuiltInIneligibleStatusWithTheStateThatRefusedIt(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunClaimerRefusesABuiltInIneligibleStatusWithTheStateThatRefusedIt(t, ctx, newEmbeddedClaimerFixture(t, "clstatus"))
}

func TestEmbeddedClaimerRefusesAWispIDAsNotFound(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunClaimerRefusesAWispIDAsNotFound(t, ctx, newEmbeddedClaimerFixture(t, "clwisp"))
}

func TestEmbeddedClaimerGrantsALiveLeaseOnTheRowItWonAndLeavesARefusedOneAlone(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunClaimerGrantsALiveLeaseOnTheRowItWonAndLeavesARefusedOneAlone(t, ctx, newEmbeddedClaimerFixture(t, "cllease"))
}

func TestEmbeddedClaimerStampsStartedAtOnceAcrossTheTwoWritesItChoosesBetween(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunClaimerStampsStartedAtOnceAcrossTheTwoWritesItChoosesBetween(t, ctx, newEmbeddedClaimerFixture(t, "clstart"))
}

func TestEmbeddedClaimerRefusesIncompleteRequestsAndAnAbsentIDWithoutTouchingState(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunClaimerRefusesIncompleteRequestsAndAnAbsentIDWithoutTouchingState(t, ctx, newEmbeddedClaimerFixture(t, "clreject"))
}

// newEmbeddedClaimerFixture composes the frozen role kit with this backend's
// claim accessor. An environment per case is affordable here — each newTestEnv
// clones a pristine template rather than booting a server — which is why this
// wiring keeps one test function per case while the unit-of-work one cannot. It
// also keeps the two config-writing cases from seeing each other's
// status.custom and claim.pools, which are workspace-global keys.
func newEmbeddedClaimerFixture(t *testing.T, prefix string) conformance.ClaimerFixture {
	t.Helper()
	te := newTestEnv(t, prefix)
	claimer, err := te.store.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
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

func TestEmbeddedClaimerRefusalMessagesCarryTheirFragments(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunClaimerRefusalMessagesCarryTheirFragments(t, ctx, newEmbeddedClaimerFixture(t, "clfrag"))
}
