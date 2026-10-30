//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_reader_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The Reader contract, run through client → in-process bd serve → reference
// store (design D8 row 1).
//
// WHAT THIS WIRING PROVES that no unit test can: the request this client
// encoded is the request the SERVER's decoder rebuilt, the role behind it is
// the same shared body a local `bd list` runs, and the page that comes back
// survives the JSON round trip. The seed handles are the reference store's, so
// a filter this client dropped answers with rows no case seeded.
//
// WHAT IS PARKED: `ListRequest.IDFilter` is the scoping tool almost every List
// case reaches for, and until S15b listIssues published no id parameter, so
// those cases parked on E-ListRequest.IDFilter. It now rides `id` behind
// issues.list.ids, which this in-process server serves, and they run as
// written. What still parks is a case whose SUBJECT (or one arm) is a member
// the wire refuses — Offset, ReadyFlag, SkipCounts — and each names that
// member's row. PinnedFlag, ExcludeTypes and WispType retired from this list
// with issues.list.filters, which this in-process server also serves. The refusals THEMSELVES are
// asserted in TestServedReaderRefusesTheFiltersTheWireCannotCarry, so the
// parking never stands in for an unpinned behaviour.

// readParkBead is the bead every READ-side park cites, and it is a different
// constant from the write side's parkBead because it is a different decision
// with a different retirement.
//
// The write parks retire when the wire grows the members it deliberately
// withholds, or never. These retire one member at a time as the wire grows a
// parameter for it; the id scope that held most of them retired with S15b.
const readParkBead = "ga-pcwtq"

func servedReaderFixture(t *testing.T, name string) conformance.ReaderFixture {
	t.Helper()
	c := composition(t)
	f := c.fixture(t)
	reader := bindRole(t, f, func(s *Store) (issueops.Reader, error) { return s.IssueReader() })
	return conformance.ReaderFixture{
		IssuePrefix:   servedIssuePrefix + "-" + name,
		Reader:        reader,
		CreateIssue:   c.seedIssue,
		CreateWisp:    c.seedIssue,
		AddDependency: c.seedDependency,
		AddComment:    c.seedComment,
		PromoteWisp:   c.seedPromotion,
	}
}

func TestServedReaderReadyDefaultTypeExclusionsYieldToAnExplicitType(t *testing.T) {
	conformance.RunReaderReadyDefaultTypeExclusionsYieldToAnExplicitType(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderReadyDeferredAndEphemeralGates(t *testing.T) {
	conformance.RunReaderReadyDeferredAndEphemeralGates(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderReadyLimitBoundary(t *testing.T) {
	conformance.RunReaderReadyLimitBoundary(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderReadySortPoliciesOrderTheSameRows(t *testing.T) {
	conformance.RunReaderReadySortPoliciesOrderTheSameRows(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// The FOUR READY cases that scope themselves with a LABEL rather than an id
// set, which is the whole reason they run here while their List siblings park:
// `labels` is a published parameter on listReadyWork and `id` is a published
// parameter on nothing.
//
// What they add over the ready cases above is the PAGE as a promise about an
// unbounded answer — every bound is asserted to be a prefix of the limitless
// read, cardinalities included — and that is a stronger claim over this wire
// than locally. A page and its unbounded reference are two round trips through
// two encodings here, so a limit this client applied on the wrong side, or a
// hydration batch the server sized differently from its page, shows up as a
// prefix that is not one.

func TestServedReaderReadyPageIsThePrefixOfTheUnboundedAnswerCountsIncluded(t *testing.T) {
	conformance.RunReaderReadyPageIsThePrefixOfTheUnboundedAnswerCountsIncluded(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderReadyPageWiderThanTheHydrationBatchIsStillThatPrefix drives
// a page wider than sqlbuild.QueryBatchSize, so the server's own hydration
// batching straddles the answer. The two rows it reads the cardinalities of are
// the last of the first batch and the first of the second.
func TestServedReaderReadyPageWiderThanTheHydrationBatchIsStillThatPrefix(t *testing.T) {
	conformance.RunReaderReadyPageWiderThanTheHydrationBatchIsStillThatPrefix(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderReadyEphemeralPageKeepsBothPlanesCountsAtItsBoundary is the
// merged-plane half: alternating durable and ephemeral rows, every Limit from
// inside the first run to past the end, and the cuts that land between a wisp
// and the durable row after it.
func TestServedReaderReadyEphemeralPageKeepsBothPlanesCountsAtItsBoundary(t *testing.T) {
	conformance.RunReaderReadyEphemeralPageKeepsBothPlanesCountsAtItsBoundary(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderReadyParentScopesToItsTransitiveDescendants(t *testing.T) {
	conformance.RunReaderReadyParentScopesToItsTransitiveDescendants(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderListParentReachesEveryDescendantAndOnlyItsOwn is the LIST
// case that needs no id scope at all — `parent` is its scope, and listIssues
// publishes it — so it is the one member of the List family here that runs
// without a park beside it.
func TestServedReaderListParentReachesEveryDescendantAndOnlyItsOwn(t *testing.T) {
	conformance.RunReaderListParentReachesEveryDescendantAndOnlyItsOwn(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderReadySetOwnsItsStatusPinnedAndTemplateDecisions(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.ReadyFlag", readParkBead,
		"the case asserts the ready set through BOTH doors, and the List --ready door is the blocker-aware "+
			"query listIssues does not publish; the Ready door's half is covered by the ready cases above")
	conformance.RunReaderReadySetOwnsItsStatusPinnedAndTemplateDecisions(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderOffsetSkipsTheRowsBeforeThePage(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.Offset", readParkBead,
		"the case's subject is Offset, which neither listIssues nor listReadyWork publishes (its id scope now "+
			"rides `id`); the Offset refusal this wire answers with instead is pinned by "+
			"TestServedReaderRefusesTheFiltersTheWireCannotCarry")
	conformance.RunReaderOffsetSkipsTheRowsBeforeThePage(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// The two pinned-flag arms ride `pinned` / `no_pinned` behind
// issues.list.filters, so the case runs as written.
func TestServedReaderListDefaultExclusionsAndTheirOverrides(t *testing.T) {
	conformance.RunReaderListDefaultExclusionsAndTheirOverrides(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListRejectsATypeOutsideTheWorkspaceVocabulary(t *testing.T) {
	conformance.RunReaderListRejectsATypeOutsideTheWorkspaceVocabulary(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListNaturalNumericIDSortTrimsAfterTheFetch(t *testing.T) {
	conformance.RunReaderListNaturalNumericIDSortTrimsAfterTheFetch(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListKeysetPositionResumesTheCreatedDescIDAscOrder(t *testing.T) {
	conformance.RunReaderListKeysetPositionResumesTheCreatedDescIDAscOrder(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListReadyFlagAnswersTheBlockerAwareSet(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.ReadyFlag", readParkBead,
		"listIssues does not publish the blocker-aware query; the ready set reaches this client through "+
			"Reader.Ready, which listReadyWork serves")
	conformance.RunReaderListReadyFlagAnswersTheBlockerAwareSet(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListReadyFlagRefusesAFilterItCannotCarry(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.ReadyFlag", readParkBead,
		"the case asserts ErrValidation naming each dropped field, which is the ready ARM's refusal; over "+
			"this wire ReadyFlag itself refuses first, so the arm is never reached")
	conformance.RunReaderListReadyFlagRefusesAFilterItCannotCarry(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListEmptyPageIsWellFormed(t *testing.T) {
	conformance.RunReaderListEmptyPageIsWellFormed(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderListQueryMatchesTheTitleOrTheID runs the search-text case as
// written: it scopes itself with a label, and Query rides listIssues' `q`
// parameter behind issues.list.search, which this in-process server serves.
func TestServedReaderListQueryMatchesTheTitleOrTheID(t *testing.T) {
	conformance.RunReaderListQueryMatchesTheTitleOrTheID(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListMaxRowsIsHonored(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.Offset", readParkBead,
		"one arm drives the cap behind an Offset, which listIssues publishes no parameter for (its id scope "+
			"now rides `id`); the cap is honored over this wire and exercised by "+
			"TestServedReaderListMaxRowsBoundsWireRowsFetched")
	conformance.RunReaderListMaxRowsIsHonored(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListSkipCountsDropsTheCardinalitiesAndNothingElse(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.SkipCounts", readParkBead,
		"the knob is DROPPED rather than refused (the wire hydrates the three cardinalities either way, and "+
			"every text rendering of `bd list` sets it, so refusing would kill the default listing over http); "+
			"its id scope rides `id` (issues.list.ids)")
	conformance.RunReaderListSkipCountsDropsTheCardinalitiesAndNothingElse(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListLimitBoundaryUnderASortTheDatabaseCanExpress(t *testing.T) {
	conformance.RunReaderListLimitBoundaryUnderASortTheDatabaseCanExpress(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListReadyFlagCarriesTheAssigneeAndPriorityFilters(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.ReadyFlag", readParkBead,
		"listIssues does not publish the blocker-aware query")
	conformance.RunReaderListReadyFlagCarriesTheAssigneeAndPriorityFilters(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderListIncludeEphemeralMergesThePlanesIntoOneOrder is the plane
// contract client wave ga-mijra brought into range. It scopes itself with
// IDFilter, which rides listIssues' `id` behind issues.list.ids (S15b), so it
// runs as written; TestServedReaderListIncludeEphemeralMergesThePlanesUnderALabelScope
// is the same fixture under a label scope, plus the plane-identity assertion
// the contract does not make.
func TestServedReaderListIncludeEphemeralMergesThePlanesIntoOneOrder(t *testing.T) {
	conformance.RunReaderListIncludeEphemeralMergesThePlanesIntoOneOrder(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// The id-scoped List cases that parked on E-ListRequest.IDFilter until S15b
// put `id` on listIssues (issues.list.ids). They run as written now; the one
// below them that still parks does so because its SUBJECT is Offset, a member
// the wire refuses.

func TestServedReaderListCountsAreBlocksOnlyWhereGetCountsEveryEdge(t *testing.T) {
	conformance.RunReaderListCountsAreBlocksOnlyWhereGetCountsEveryEdge(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListKeysetPositionNarrowsWithoutReplacingTheOtherPredicates(t *testing.T) {
	conformance.RunReaderListKeysetPositionNarrowsWithoutReplacingTheOtherPredicates(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListKeysetWalkOverAnOversizedGroupLosesNothingAndRepeatsNothing(t *testing.T) {
	conformance.RunReaderListKeysetWalkOverAnOversizedGroupLosesNothingAndRepeatsNothing(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// The priority-order walk over an oversized equal-key run, page after page:
// the sort=priority pushdown (TestServedReaderListFallsBackToTheWalkWithoutTheCapability)
// and one resumed priority keyset position
// (TestServedReaderListPriorityKeysetPositionResumesThePriorityOrder) do not
// cover the WALK, which this id-scoped case drives now that `id` is served.
func TestServedReaderListPriorityKeysetWalkOverAnOversizedEqualKeyRunLosesNothingAndRepeatsNothing(t *testing.T) {
	conformance.RunReaderListPriorityKeysetWalkOverAnOversizedEqualKeyRunLosesNothingAndRepeatsNothing(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderListMaxRowsBoundaryIsLimitPlusOffset parks on OFFSET: the
// whole point is to drive the cap along the OFFSET
// axis with the limit held still, and listIssues publishes no offset parameter.
// The LIMIT axis of the same boundary runs over this wire in
// TestServedReaderListMaxRowsBoundsWireRowsFetched.
func TestServedReaderListMaxRowsBoundaryIsLimitPlusOffset(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.Offset", readParkBead,
		"the case moves the cap along the Offset axis, and listIssues publishes no offset parameter; "+
			"the cap's Limit axis is driven over this wire by "+
			"TestServedReaderListMaxRowsBoundsWireRowsFetched")
	conformance.RunReaderListMaxRowsBoundaryIsLimitPlusOffset(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderListWispTypeNarrowsTheAdmittedPlaneRatherThanAdmittingIt runs
// as written: the classification predicate rides `wisp_type` behind
// issues.list.filters beside the served IncludeEphemeral plane knob.
func TestServedReaderListWispTypeNarrowsTheAdmittedPlaneRatherThanAdmittingIt(t *testing.T) {
	conformance.RunReaderListWispTypeNarrowsTheAdmittedPlaneRatherThanAdmittingIt(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// The list filter families behind issues.list.filters, run through the
// client's encoder and the server's decode: the eight time bounds at their
// exact boundaries (whole-second, strict, NULL never matching, an offset bound
// equal to its UTC twin) and one included and one excluded row per remaining
// family.
func TestServedReaderListTimeBoundsAreStrictAndWholeSecond(t *testing.T) {
	conformance.RunReaderListTimeBoundsAreStrictAndWholeSecond(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListFilterFamiliesNarrowToTheirRows(t *testing.T) {
	conformance.RunReaderListFilterFamiliesNarrowToTheirRows(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListStatusAcceptsACommaSeparatedORSet(t *testing.T) {
	conformance.RunReaderListStatusAcceptsACommaSeparatedORSet(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// The THREE BRIEF CONTRACTS, adopted by client wave ga-f352s.
//
// They were reachable before they were wired, and the gap was one thing rather
// than three: `brief` is a published parameter on both pages (#5586) and
// `brief_deps` on getIssue (#5546), and this client's encoder sends all three —
// so the server has been leaving the text columns unselected for a wave already.
// What was missing is the MARKER. types.Issue.IsLitePartial is `json:"-"`, so a
// projected row arrives byte-identical to a genuinely textless one, and
// assertReaderBriefRow hard-fails any leg that answers one with the flag unset.
//
// THE CONTRACT IS WHAT FORCED THE STAMP, which is why deferring these to this
// wave was safe rather than lucky: there is no way to adopt them and not stamp,
// and no way to stamp and leave a door out — the client sends the parameter from
// three places (listTable, readyTable and the ready bridge) and all three lift
// their rows through one function. TestBriefListingsStampTheProjectionMarker
// asserts that door-by-door; these assert what a projected row must contain.

// TestServedReaderListBriefDropsTheFreeFormTextAndNothingElse scopes itself
// with IDFilter, which rides `id` (issues.list.ids), and the ready sibling below
// drives the identical assertReaderBriefRow under a label scope.
func TestServedReaderListBriefDropsTheFreeFormTextAndNothingElse(t *testing.T) {
	conformance.RunReaderListBriefDropsTheFreeFormTextAndNothingElse(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderReadyBriefDropsTheFreeFormTextAndNothingElse(t *testing.T) {
	conformance.RunReaderReadyBriefDropsTheFreeFormTextAndNothingElse(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderGetBriefDepsProjectsTheDependencyRows is the third, and the
// one that needs no marker at all: `brief_deps` projects the rows of the
// DEPENDENCIES member, and the contract asserts the identity fields survive and
// the free-form text is gone. The detail view's own row is untouched by it.
func TestServedReaderGetBriefDepsProjectsTheDependencyRows(t *testing.T) {
	conformance.RunReaderGetBriefDepsProjectsTheDependencyRows(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderGetResolvesTheExactIDAcrossBothPlanes(t *testing.T) {
	conformance.RunReaderGetResolvesTheExactIDAcrossBothPlanes(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderGetMissIsNotFoundAndBackendFailureDoesNotDecay(t *testing.T) {
	conformance.RunReaderGetMissIsNotFoundAndBackendFailureDoesNotDecay(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderGetOptionalRowListsAreOffByDefault is the shared proof of the
// L4 retirement: the `include_dependents` and `include_comments` parameters this
// client sends are what put the two row lists on `bd show`'s detail view.
func TestServedReaderGetOptionalRowListsAreOffByDefault(t *testing.T) {
	conformance.RunReaderGetOptionalRowListsAreOffByDefault(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderGetDetailShapeMatchesTheSeededIssue(t *testing.T) {
	conformance.RunReaderGetDetailShapeMatchesTheSeededIssue(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderGetPopulatesRowVersionForAGuardedWrite is HIGH 5's pin.
//
// types.Issue.RowVersion is json:"-" (internal/types/types.go), so a bare
// decode of getIssue's response body leaves it at zero; the wire's only
// spelling of the token is the detail view's sibling `revision` string, which
// Reader.Get must stitch back on. A read alone cannot tell a stitched zero
// from a real one that happens to equal it on a fresh table, so this proves it
// the way a caller actually consumes RowVersion: by GUARDING A WRITE with
// whatever Get just answered, over the same wire, and requiring the guard to
// be real rather than decorative.
//
// The second Update reuses the SAME (now-stale) token. If Get had answered 0
// — the bug this pins against — the first Update's own post-write token would
// also have nothing to do with it, and there would be no way for this case to
// tell "the guard matched" from "the guard was never checked". Requiring the
// REUSE to refuse is what rules that out: it only refuses if the first write
// really did move the row past the exact version Get reported.
func TestServedReaderGetPopulatesRowVersionForAGuardedWrite(t *testing.T) {
	env := newServedEnv(t, "rdrv")
	reader, err := env.subject.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	issue := &types.Issue{Title: "row version round trip", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := env.createIssue(t.Context(), issue, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	details, err := reader.Get(t.Context(), issueops.GetRequest{ID: issue.ID})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if details.RowVersion == 0 {
		t.Fatalf("Get answered RowVersion 0 (Revision %q); want the row's real token", details.Revision)
	}
	staleVersion := details.RowVersion

	if _, err := lifecycle.Update(t.Context(), issueops.UpdateRequest{
		Actor: "writer", IssueID: issue.ID, ExpectedVersion: &staleVersion,
		Patch: issueops.IssuePatch{Title: set("first guarded write")},
	}); err != nil {
		t.Fatalf("Update guarded by the token Get answered: %v", err)
	}

	_, err = lifecycle.Update(t.Context(), issueops.UpdateRequest{
		Actor: "writer", IssueID: issue.ID, ExpectedVersion: &staleVersion,
		Patch: issueops.IssuePatch{Title: set("second guarded write, same stale token")},
	})
	if err == nil {
		t.Fatal("Update reused Get's token after a write moved the row past it; want a version-guard refusal")
	}
}

// The List leg sets ExcludeTypes, which rides `exclude_type` behind
// issues.list.filters, so the case runs as written.
func TestServedReaderDoesNotMutateTheCallerRequest(t *testing.T) {
	conformance.RunReaderDoesNotMutateTheCallerRequest(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderListExcludeStatusAcceptsAWorkspaceCustomStatus is the
// served twin of the count's custom-status pin: ListRequest.ExcludeStatus is
// validated against the SERVER workspace's own statuses (status.custom), so a
// custom status is accepted and applied over the wire, and a name in no
// vocabulary is still refused. A leg test rather than a conformance case
// because ReaderFixture has no way to configure a workspace custom status.
func TestServedReaderListExcludeStatusAcceptsAWorkspaceCustomStatus(t *testing.T) {
	e := newServedEnv(t, "lcs")
	ctx := t.Context()
	if err := e.setConfig(ctx, "status.custom", "review,qa"); err != nil {
		t.Fatalf("SetConfig(status.custom): %v", err)
	}
	const openID, reviewID = "lcs-open", "lcs-review"
	for _, id := range []string{openID, reviewID} {
		if err := e.createIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	if err := e.reference.UpdateIssue(ctx, reviewID, map[string]interface{}{"status": types.Status("review")}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(%s) to review: %v", reviewID, err)
	}
	reader, err := e.subject.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}

	scope := issueops.ListRequest{IDFilter: openID + "," + reviewID}
	page, err := reader.List(ctx, scope)
	if err != nil {
		t.Fatalf("List(unfiltered): %v", err)
	}
	if got := pageIDs(page); len(got) != 2 {
		t.Fatalf("List(unfiltered) = %v, want both rows (a custom status with no category is not a default exclusion)", got)
	}

	scope.ExcludeStatus = []string{"review"}
	page, err = reader.List(ctx, scope)
	if err != nil {
		t.Fatalf("List(ExcludeStatus=review): %v, want the custom status accepted", err)
	}
	if got := pageIDs(page); len(got) != 1 || got[0] != openID {
		t.Fatalf("List(ExcludeStatus=review) = %v, want [%s]", got, openID)
	}

	scope.ExcludeStatus = []string{"reveiw"}
	if _, err := reader.List(ctx, scope); err == nil {
		t.Fatal("List(ExcludeStatus=reveiw) answered instead of refusing a status in no vocabulary")
	}
}
