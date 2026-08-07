package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The Reader contract, wired at the unit-of-work backend. This is the one
// wiring that runs a genuinely separate body: the two store accessors both hand
// back workapi/storereader.New(store), while internal/storage/uow/issue_reader.go
// builds its own filter, runs its own seam and feeds the shared epilogue a
// native has-more verdict instead of an over-fetched row.
//
// ONE PROVIDER, NAMESPACED SUBTESTS. Each newTestUOWProvider boots a real Dolt
// sql-server (measured at ~5s per boot on this machine, plus a one-time binary
// build), so a fixture per case would make this leg minutes long by itself. The
// conformance fixtures were designed for sharing — every case scopes its query
// to the rows it seeded, by id set, by label, or by exact id — so one database
// serves the suite. The subtests are deliberately NOT parallel: they share the
// database.

func TestReaderContract(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "rdr")
	fixture := newUOWReaderFixture(t, provider, "rdr")

	t.Run("ReadyDefaultTypeExclusionsYieldToAnExplicitType", func(t *testing.T) {
		conformance.RunReaderReadyDefaultTypeExclusionsYieldToAnExplicitType(t, ctx, fixture)
	})
	t.Run("ReadyDeferredAndEphemeralGates", func(t *testing.T) {
		conformance.RunReaderReadyDeferredAndEphemeralGates(t, ctx, fixture)
	})
	t.Run("ReadyLimitBoundary", func(t *testing.T) {
		conformance.RunReaderReadyLimitBoundary(t, ctx, fixture)
	})
	t.Run("OffsetIsHonoredOrRefused", func(t *testing.T) {
		conformance.RunReaderOffsetIsHonoredOrRefused(t, ctx, fixture)
	})
	// The REFUSING arm of MaxRows, and the mirror image of the wiring above:
	// this body honors Offset and refuses the cap, the store-backed one honors
	// the cap and refuses Offset. Neither answers a question it cannot answer.
	t.Run("ListMaxRowsIsHonoredOrRefused", func(t *testing.T) {
		conformance.RunReaderListMaxRowsIsHonoredOrRefused(t, ctx, fixture)
	})
	t.Run("ListSkipCountsDropsTheCardinalitiesAndNothingElse", func(t *testing.T) {
		conformance.RunReaderListSkipCountsDropsTheCardinalitiesAndNothingElse(t, ctx, fixture)
	})
	t.Run("ReadySortPoliciesOrderTheSameRows", func(t *testing.T) {
		conformance.RunReaderReadySortPoliciesOrderTheSameRows(t, ctx, fixture)
	})
	t.Run("ListDefaultExclusionsAndTheirOverrides", func(t *testing.T) {
		conformance.RunReaderListDefaultExclusionsAndTheirOverrides(t, ctx, fixture)
	})
	t.Run("ListRejectsATypeOutsideTheWorkspaceVocabulary", func(t *testing.T) {
		conformance.RunReaderListRejectsATypeOutsideTheWorkspaceVocabulary(t, ctx, fixture)
	})
	t.Run("ListNaturalNumericIDSortTrimsAfterTheFetch", func(t *testing.T) {
		conformance.RunReaderListNaturalNumericIDSortTrimsAfterTheFetch(t, ctx, fixture)
	})
	t.Run("ListLimitBoundaryUnderASortTheDatabaseCanExpress", func(t *testing.T) {
		conformance.RunReaderListLimitBoundaryUnderASortTheDatabaseCanExpress(t, ctx, fixture)
	})
	t.Run("ReadySetOwnsItsStatusPinnedAndTemplateDecisions", func(t *testing.T) {
		conformance.RunReaderReadySetOwnsItsStatusPinnedAndTemplateDecisions(t, ctx, fixture)
	})
	t.Run("ListReadyFlagCarriesTheAssigneeAndPriorityFilters", func(t *testing.T) {
		conformance.RunReaderListReadyFlagCarriesTheAssigneeAndPriorityFilters(t, ctx, fixture)
	})
	t.Run("ListStatusAcceptsACommaSeparatedORSet", func(t *testing.T) {
		conformance.RunReaderListStatusAcceptsACommaSeparatedORSet(t, ctx, fixture)
	})
	t.Run("ListKeysetPositionResumesTheCreatedDescIDAscOrder", func(t *testing.T) {
		conformance.RunReaderListKeysetPositionResumesTheCreatedDescIDAscOrder(t, ctx, fixture)
	})
	t.Run("ListReadyFlagAnswersTheBlockerAwareSet", func(t *testing.T) {
		conformance.RunReaderListReadyFlagAnswersTheBlockerAwareSet(t, ctx, fixture)
	})
	t.Run("ListReadyFlagRefusesAFilterItCannotCarry", func(t *testing.T) {
		conformance.RunReaderListReadyFlagRefusesAFilterItCannotCarry(t, ctx, fixture)
	})
	t.Run("ListEmptyPageIsWellFormed", func(t *testing.T) {
		conformance.RunReaderListEmptyPageIsWellFormed(t, ctx, fixture)
	})
	t.Run("GetResolvesTheExactIDAcrossBothPlanes", func(t *testing.T) {
		conformance.RunReaderGetResolvesTheExactIDAcrossBothPlanes(t, ctx, fixture)
	})
	t.Run("GetOptionalRowListsAreOffByDefault", func(t *testing.T) {
		conformance.RunReaderGetOptionalRowListsAreOffByDefault(t, ctx, fixture)
	})
	t.Run("GetDetailShapeMatchesTheSeededIssue", func(t *testing.T) {
		conformance.RunReaderGetDetailShapeMatchesTheSeededIssue(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunReaderDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
	// Last on purpose: the backend-failure half runs a request on a dead
	// context, and this backend's provider is shared by every case above it.
	t.Run("GetMissIsNotFoundAndBackendFailureDoesNotDecay", func(t *testing.T) {
		conformance.RunReaderGetMissIsNotFoundAndBackendFailureDoesNotDecay(t, ctx, fixture)
	})
}

// newUOWReaderFixture composes the shared role kit with the reader accessor.
//
// The role is reached through the provider's ACCESSOR, not NewIssueReader: the
// accessor is the door on this seam exactly as store.IssueReader() is on the
// other, and a wiring that called the constructor would be testing a reader no
// caller can obtain.
func newUOWReaderFixture(t *testing.T, provider UnitOfWorkProvider, prefix string) conformance.ReaderFixture {
	t.Helper()
	source, ok := provider.(IssueReaderSource)
	if !ok {
		t.Fatalf("provider %T does not offer the IssueReader accessor", provider)
	}
	reader, err := source.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.ReaderFixture{
		IssuePrefix:   kit.IssuePrefix,
		Reader:        reader,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		// Not on the kit; see the same note in the server-backed wiring.
		AddComment: func(ctx context.Context, issueID, author, text string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				_, err := uw.CommentUseCase().AddCommentToIssue(ctx, issueID, author, text)
				return "bd: comment " + issueID, err
			})
		},
	}
}
