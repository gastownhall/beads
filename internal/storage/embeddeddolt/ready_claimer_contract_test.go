//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

func TestEmbeddedReadyClaimerRejectsLimitOffsetAndEmptyActor(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReadyClaimerRejectsLimitOffsetAndEmptyActor(t, ctx, newEmbeddedReadyClaimerFixture(t, "rcrej"))
}

func TestEmbeddedReadyClaimerEmptyFrontIsNormal(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReadyClaimerEmptyFrontIsNormal(t, ctx, newEmbeddedReadyClaimerFixture(t, "rcempty"))
}

func TestEmbeddedReadyClaimerClaimsTheFrontRowAndReturnsThePostClaimState(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReadyClaimerClaimsTheFrontRowAndReturnsThePostClaimState(t, ctx, newEmbeddedReadyClaimerFixture(t, "rcwin"))
}

func TestEmbeddedReadyClaimerClaimsAnEphemeralRowTheFilterAdmits(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReadyClaimerClaimsAnEphemeralRowTheFilterAdmits(t, ctx, newEmbeddedReadyClaimerFixture(t, "rcwisp"))
}

func TestEmbeddedReadyClaimerLeavesEphemeralRowsOutOfTheDefaultReadySet(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReadyClaimerLeavesEphemeralRowsOutOfTheDefaultReadySet(t, ctx, newEmbeddedReadyClaimerFixture(t, "rcdefault"))
}

func TestEmbeddedReadyClaimerLeasesADurableWinButNotAnEphemeralOne(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReadyClaimerLeasesADurableWinButNotAnEphemeralOne(t, ctx, newEmbeddedReadyClaimerFixture(t, "rclease"))
}

func TestEmbeddedReadyClaimerAnswersTheQuestionReaderReadyLists(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReadyClaimerAnswersTheQuestionReaderReadyLists(t, ctx, newEmbeddedReadyClaimerFixture(t, "rcagree"))
}

func TestEmbeddedReadyClaimerSkipsIneligibleFrontRows(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReadyClaimerSkipsIneligibleFrontRows(t, ctx, newEmbeddedReadyClaimerFixture(t, "rcskip"))
}

func TestEmbeddedReadyClaimerFencesTheClaimByEveryLabelSetAndTheParentItWasGiven(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReadyClaimerFencesTheClaimByEveryLabelSetAndTheParentItWasGiven(t, ctx, newEmbeddedReadyClaimerFixture(t, "rcfence"))
}

func TestEmbeddedReadyClaimerHydratesOnlyItsBlocksEdgesIntoTheCardinalities(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReadyClaimerHydratesOnlyItsBlocksEdgesIntoTheCardinalities(t, ctx, newEmbeddedReadyClaimerFixture(t, "rccount"))
}

func TestEmbeddedReadyClaimerRecordsOneHistoryEntryForAWin(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReadyClaimerRecordsOneHistoryEntryForAWin(t, ctx, newEmbeddedReadyClaimerFixture(t, "rchist"))
}

func TestEmbeddedReadyClaimerDoesNotMutateTheCallerRequest(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReadyClaimerDoesNotMutateTheCallerRequest(t, ctx, newEmbeddedReadyClaimerFixture(t, "rcsnap"))
}

// newEmbeddedReadyClaimerFixture composes the frozen role kit with this
// backend's two accessors. An environment per case is affordable here — each
// newTestEnv clones a pristine template rather than booting a server — which is
// why this wiring keeps one test function per case while the unit-of-work one
// cannot.
func newEmbeddedReadyClaimerFixture(t *testing.T, prefix string) conformance.ReadyClaimerFixture {
	t.Helper()
	te := newTestEnv(t, prefix)
	claimer, err := te.store.ReadyClaimer()
	if err != nil {
		t.Fatalf("ReadyClaimer(): %v", err)
	}
	reader, err := te.store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
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
