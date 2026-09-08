//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The Reader contract, wired at the embedded store. This accessor returns the
// same workapi/storereader body the server-backed store's does, so what this
// wiring buys is the ENGINE and the connection wrapper underneath it, not a
// second opinion on the body. It is still worth running: engine-level
// disagreement is the class the embedded wirings have caught before.

func TestEmbeddedReaderReadyDefaultTypeExclusionsYieldToAnExplicitType(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderReadyDefaultTypeExclusionsYieldToAnExplicitType(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderReadyDeferredAndEphemeralGates(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderReadyDeferredAndEphemeralGates(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderReadyLimitBoundary(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderReadyLimitBoundary(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderOffsetSkipsTheRowsBeforeThePage(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderOffsetSkipsTheRowsBeforeThePage(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

// SkipCounts matters most HERE. The aggregate it drops is the reverse-blocker
// join, whose COALESCE key the pure-Go analyzer cannot auto-index — so this is
// the engine where the knob is worth having, and the one where an
// implementation that quietly ignored it would be least visible.
func TestEmbeddedReaderListMaxRowsIsHonored(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListMaxRowsIsHonored(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListMaxRowsBoundaryIsLimitPlusOffset(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListMaxRowsBoundaryIsLimitPlusOffset(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListSkipCountsDropsTheCardinalitiesAndNothingElse(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListSkipCountsDropsTheCardinalitiesAndNothingElse(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderReadySortPoliciesOrderTheSameRows(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderReadySortPoliciesOrderTheSameRows(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListDefaultExclusionsAndTheirOverrides(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListDefaultExclusionsAndTheirOverrides(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListRejectsATypeOutsideTheWorkspaceVocabulary(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListRejectsATypeOutsideTheWorkspaceVocabulary(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListNaturalNumericIDSortTrimsAfterTheFetch(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListNaturalNumericIDSortTrimsAfterTheFetch(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListLimitBoundaryUnderASortTheDatabaseCanExpress(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListLimitBoundaryUnderASortTheDatabaseCanExpress(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderReadySetOwnsItsStatusPinnedAndTemplateDecisions(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderReadySetOwnsItsStatusPinnedAndTemplateDecisions(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListReadyFlagCarriesTheAssigneeAndPriorityFilters(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListReadyFlagCarriesTheAssigneeAndPriorityFilters(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListStatusAcceptsACommaSeparatedORSet(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListStatusAcceptsACommaSeparatedORSet(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListKeysetPositionResumesTheCreatedDescIDAscOrder(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListKeysetPositionResumesTheCreatedDescIDAscOrder(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListReadyFlagAnswersTheBlockerAwareSet(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListReadyFlagAnswersTheBlockerAwareSet(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListReadyFlagRefusesAFilterItCannotCarry(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListReadyFlagRefusesAFilterItCannotCarry(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListEmptyPageIsWellFormed(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListEmptyPageIsWellFormed(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderGetResolvesTheExactIDAcrossBothPlanes(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderGetResolvesTheExactIDAcrossBothPlanes(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderGetMissIsNotFoundAndBackendFailureDoesNotDecay(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderGetMissIsNotFoundAndBackendFailureDoesNotDecay(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderGetOptionalRowListsAreOffByDefault(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderGetOptionalRowListsAreOffByDefault(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderGetBriefDepsProjectsTheDependencyRows(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderGetBriefDepsProjectsTheDependencyRows(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderGetDetailShapeMatchesTheSeededIssue(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderGetDetailShapeMatchesTheSeededIssue(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderDoesNotMutateTheCallerRequest(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderDoesNotMutateTheCallerRequest(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

// One body, the other engine. The counts mega-query's reverse-blocker join is
// the one the pure-Go planner cannot index, which is why the by-ids page-down
// path these cases drive exists at all — so an engine-level disagreement about
// it shows here rather than at the server-backed wiring.
func TestEmbeddedReaderReadyPageIsThePrefixOfTheUnboundedAnswerCountsIncluded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderReadyPageIsThePrefixOfTheUnboundedAnswerCountsIncluded(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderReadyEphemeralPageKeepsBothPlanesCountsAtItsBoundary(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderReadyEphemeralPageKeepsBothPlanesCountsAtItsBoundary(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderReadyPageWiderThanTheHydrationBatchIsStillThatPrefix(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderReadyPageWiderThanTheHydrationBatchIsStillThatPrefix(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListCountsAreBlocksOnlyWhereGetCountsEveryEdge(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListCountsAreBlocksOnlyWhereGetCountsEveryEdge(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderReadyParentScopesToItsTransitiveDescendants(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderReadyParentScopesToItsTransitiveDescendants(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

// The id prefix is a LIKE against a binary-collated column on this engine, so
// the cased-sibling half of the case has its most plausible failure mode here.
func TestEmbeddedReaderListParentReachesEveryDescendantAndOnlyItsOwn(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListParentReachesEveryDescendantAndOnlyItsOwn(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListKeysetWalkOverAnOversizedGroupLosesNothingAndRepeatsNothing(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListKeysetWalkOverAnOversizedGroupLosesNothingAndRepeatsNothing(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListPriorityKeysetWalkOverAnOversizedEqualKeyRunLosesNothingAndRepeatsNothing(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListPriorityKeysetWalkOverAnOversizedEqualKeyRunLosesNothingAndRepeatsNothing(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListKeysetPositionNarrowsWithoutReplacingTheOtherPredicates(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListKeysetPositionNarrowsWithoutReplacingTheOtherPredicates(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListIncludeEphemeralMergesThePlanesIntoOneOrder(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListIncludeEphemeralMergesThePlanesIntoOneOrder(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListWispTypeNarrowsTheAdmittedPlaneRatherThanAdmittingIt(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListWispTypeNarrowsTheAdmittedPlaneRatherThanAdmittingIt(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderListBriefDropsTheFreeFormTextAndNothingElse(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderListBriefDropsTheFreeFormTextAndNothingElse(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

func TestEmbeddedReaderReadyBriefDropsTheFreeFormTextAndNothingElse(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunReaderReadyBriefDropsTheFreeFormTextAndNothingElse(t, ctx, newEmbeddedReaderFixture(t, "rdr"))
}

// newEmbeddedReaderFixture composes the shared role kit with the reader
// accessor. One environment per case: newTestEnv clones a pristine template
// into the test's own temp dir, which costs a fraction of a second once the
// template exists.
func newEmbeddedReaderFixture(t *testing.T, prefix string) conformance.ReaderFixture {
	t.Helper()
	te := newTestEnv(t, prefix)
	reader, err := te.store.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.ReaderFixture{
		IssuePrefix:   kit.IssuePrefix,
		Reader:        reader,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		// Not on the kit; see the same note in the server-backed wiring.
		AddComment: func(ctx context.Context, issueID, author, text string) error {
			_, err := te.store.AddIssueComment(ctx, issueID, author, text)
			return err
		},
	}
}
