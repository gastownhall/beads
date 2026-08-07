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

func TestReaderOffsetIsHonoredOrRefused(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderOffsetIsHonoredOrRefused(t, ctx, fixture)
}

// The store-backed body is the arm that HONORS MaxRows: the cap rides the
// filter the shared builder produces and the search path enforces it after the
// scan. The refusing arm is the unit-of-work wiring.
func TestReaderListMaxRowsIsHonoredOrRefused(t *testing.T) {
	fixture, ctx, cleanup := newDoltReaderFixture(t, "rdr")
	defer cleanup()
	conformance.RunReaderListMaxRowsIsHonoredOrRefused(t, ctx, fixture)
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
