package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The Reader contract, wired at the server-backed store. This accessor and the
// embedded one both return workapi/storereader.New(store), so these two wirings
// run ONE body against two engines; the third wiring
// (internal/storage/uow/reader_contract_test.go) is the genuinely separate
// implementation. A case passing here and on the embedded store is one vote,
// not two.

func TestReaderReadyDefaultTypeExclusionsYieldToAnExplicitType(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderReadyDefaultTypeExclusionsYieldToAnExplicitType(t, ctx, fixture)
}

func TestReaderReadyDeferredAndEphemeralGates(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderReadyDeferredAndEphemeralGates(t, ctx, fixture)
}

func TestReaderReadyLimitBoundary(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderReadyLimitBoundary(t, ctx, fixture)
}

func TestReaderOffsetSkipsTheRowsBeforeThePage(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderOffsetSkipsTheRowsBeforeThePage(t, ctx, fixture)
}

// The cap rides the filter the shared builder produces and the search path
// enforces it after the scan. It used to be the only arm that did: the
// unit-of-work wiring refused the field, and the case accepted either answer.
func TestReaderListMaxRowsIsHonored(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListMaxRowsIsHonored(t, ctx, fixture)
}

// The cap's boundary along the OFFSET axis. It is the composition this body
// reaches in two steps — widen the filter, then size the probe row — that the
// case checks, and no request without an offset can see it go wrong.
func TestReaderListMaxRowsBoundaryIsLimitPlusOffset(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListMaxRowsBoundaryIsLimitPlusOffset(t, ctx, fixture)
}

func TestReaderListSkipCountsDropsTheCardinalitiesAndNothingElse(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListSkipCountsDropsTheCardinalitiesAndNothingElse(t, ctx, fixture)
}

func TestReaderReadySortPoliciesOrderTheSameRows(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderReadySortPoliciesOrderTheSameRows(t, ctx, fixture)
}

func TestReaderListDefaultExclusionsAndTheirOverrides(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListDefaultExclusionsAndTheirOverrides(t, ctx, fixture)
}

func TestReaderListRejectsATypeOutsideTheWorkspaceVocabulary(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListRejectsATypeOutsideTheWorkspaceVocabulary(t, ctx, fixture)
}

func TestReaderListNaturalNumericIDSortTrimsAfterTheFetch(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListNaturalNumericIDSortTrimsAfterTheFetch(t, ctx, fixture)
}

func TestReaderListLimitBoundaryUnderASortTheDatabaseCanExpress(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListLimitBoundaryUnderASortTheDatabaseCanExpress(t, ctx, fixture)
}

func TestReaderReadySetOwnsItsStatusPinnedAndTemplateDecisions(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderReadySetOwnsItsStatusPinnedAndTemplateDecisions(t, ctx, fixture)
}

func TestReaderListReadyFlagCarriesTheAssigneeAndPriorityFilters(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListReadyFlagCarriesTheAssigneeAndPriorityFilters(t, ctx, fixture)
}

func TestReaderListStatusAcceptsACommaSeparatedORSet(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListStatusAcceptsACommaSeparatedORSet(t, ctx, fixture)
}

func TestReaderListKeysetPositionResumesTheCreatedDescIDAscOrder(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListKeysetPositionResumesTheCreatedDescIDAscOrder(t, ctx, fixture)
}

func TestReaderListReadyFlagAnswersTheBlockerAwareSet(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListReadyFlagAnswersTheBlockerAwareSet(t, ctx, fixture)
}

func TestReaderListReadyFlagRefusesAFilterItCannotCarry(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListReadyFlagRefusesAFilterItCannotCarry(t, ctx, fixture)
}

func TestReaderListEmptyPageIsWellFormed(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListEmptyPageIsWellFormed(t, ctx, fixture)
}

func TestReaderGetResolvesTheExactIDAcrossBothPlanes(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderGetResolvesTheExactIDAcrossBothPlanes(t, ctx, fixture)
}

func TestReaderGetMissIsNotFoundAndBackendFailureDoesNotDecay(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderGetMissIsNotFoundAndBackendFailureDoesNotDecay(t, ctx, fixture)
}

func TestReaderGetOptionalRowListsAreOffByDefault(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderGetOptionalRowListsAreOffByDefault(t, ctx, fixture)
}

func TestReaderGetBriefDepsProjectsTheDependencyRows(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderGetBriefDepsProjectsTheDependencyRows(t, ctx, fixture)
}

func TestReaderGetDetailShapeMatchesTheSeededIssue(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderGetDetailShapeMatchesTheSeededIssue(t, ctx, fixture)
}

func TestReaderDoesNotMutateTheCallerRequest(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderDoesNotMutateTheCallerRequest(t, ctx, fixture)
}

// The bounded ready page and the unbounded one are two different queries on
// this body — an id page plus a by-ids hydration against the predicate-form
// mega-query (internal/storage/issueops/ready_work_counts.go) — so the identity
// the case asserts is a claim about this wiring specifically, not a tautology.
func TestReaderReadyPageIsThePrefixOfTheUnboundedAnswerCountsIncluded(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderReadyPageIsThePrefixOfTheUnboundedAnswerCountsIncluded(t, ctx, fixture)
}

// The plane union is a Go-side merge of two per-family query results here,
// where the unit-of-work wiring orders one UNION ALL in SQL.
func TestReaderReadyEphemeralPageKeepsBothPlanesCountsAtItsBoundary(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderReadyEphemeralPageKeepsBothPlanesCountsAtItsBoundary(t, ctx, fixture)
}

func TestReaderReadyPageWiderThanTheHydrationBatchIsStillThatPrefix(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderReadyPageWiderThanTheHydrationBatchIsStillThatPrefix(t, ctx, fixture)
}

// The two count vocabularies reach two different store methods here: the page
// rides sqlbuild's mega-query and the detail view rides CountDependencies /
// CountDependents, which count every edge type.
func TestReaderListCountsAreBlocksOnlyWhereGetCountsEveryEdge(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListCountsAreBlocksOnlyWhereGetCountsEveryEdge(t, ctx, fixture)
}

func TestReaderReadyParentScopesToItsTransitiveDescendants(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderReadyParentScopesToItsTransitiveDescendants(t, ctx, fixture)
}

func TestReaderListParentReachesEveryDescendantAndOnlyItsOwn(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListParentReachesEveryDescendantAndOnlyItsOwn(t, ctx, fixture)
}

// The walk is where this body's probe-row over-fetch has to stay out of the
// caller's way: the next position comes from the last DELIVERED row, and the
// probe row is not one.
func TestReaderListKeysetWalkOverAnOversizedGroupLosesNothingAndRepeatsNothing(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListKeysetWalkOverAnOversizedGroupLosesNothingAndRepeatsNothing(t, ctx, fixture)
}

// The same probe-row rule under the second served order, where the next
// position is a triple and the row it is read from is still the last DELIVERED
// one.
func TestReaderListPriorityKeysetWalkOverAnOversizedEqualKeyRunLosesNothingAndRepeatsNothing(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListPriorityKeysetWalkOverAnOversizedEqualKeyRunLosesNothingAndRepeatsNothing(t, ctx, fixture)
}

func TestReaderListKeysetPositionNarrowsWithoutReplacingTheOtherPredicates(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListKeysetPositionNarrowsWithoutReplacingTheOtherPredicates(t, ctx, fixture)
}

// The merge arrangement this body actually uses: two independently ordered
// legs, re-sorted in Go and then trimmed. The bounded arm is where that order
// has to be applied BEFORE the trim, and the walk is where the probe-row
// over-fetch has to stay off the next position on both planes at once.
func TestReaderListIncludeEphemeralMergesThePlanesIntoOneOrder(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListIncludeEphemeralMergesThePlanesIntoOneOrder(t, ctx, fixture)
}

func TestReaderListWispTypeNarrowsTheAdmittedPlaneRatherThanAdmittingIt(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListWispTypeNarrowsTheAdmittedPlaneRatherThanAdmittingIt(t, ctx, fixture)
}

func TestReaderListBriefDropsTheFreeFormTextAndNothingElse(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListBriefDropsTheFreeFormTextAndNothingElse(t, ctx, fixture)
}

func TestReaderReadyBriefDropsTheFreeFormTextAndNothingElse(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderReadyBriefDropsTheFreeFormTextAndNothingElse(t, ctx, fixture)
}

// newDoltReaderFixture composes the shared role kit with the reader accessor.
// One store per case here rather than one per suite: setupTestStore gives each
// test its own copy-on-write branch and costs a fraction of a second, so the
// isolation is nearly free — unlike the unit-of-work wiring, where a provider
// boot is seconds and the suite has to share one.
func newDoltReaderFixture(t *testing.T, prefix string) (conformance.ReaderFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	reader, err := store.IssueReader()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("IssueReader(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.ReaderFixture{
		IssuePrefix:   kit.IssuePrefix,
		Reader:        reader,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		// Not on the kit: the role fixtures the scaffolding slice froze have no
		// comment hook, and the Get detail view's comment count and opt-in
		// comment rows cannot be asserted without one. Supplied locally rather
		// than by editing the frozen kit.
		AddComment: func(ctx context.Context, issueID, author, text string) error {
			_, err := store.AddIssueComment(ctx, issueID, author, text)
			return err
		},
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
