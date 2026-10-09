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
// WHAT IS PARKED, and it is one cause with many names: `ListRequest.IDFilter`
// is the scoping tool almost every List case reaches for, and listIssues
// publishes no id parameter (E-ListRequest.IDFilter). Refusing it is correct —
// dropping it would answer about the whole workspace — but a case that scopes
// itself with it cannot run over this wire at all. Each parked case names the
// field that refused, and the refusal ITSELF is asserted below in
// TestServedReaderRefusesTheFiltersTheWireCannotCarry, so the parking never
// stands in for an unpinned behaviour.

// readParkBead is the bead every READ-side park cites, and it is a different
// constant from the write side's parkBead because it is a different decision
// with a different retirement.
//
// The write parks retire when the wire grows the members it deliberately
// withholds, or never. These retire when the wire grows a way to SCOPE a
// listing by id — the upstream ask D8 row 1 records alongside the `sort`
// parameter — at which point almost all of them go at once. Folding the two
// into one constant would have said they move together, which they do not.
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
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the List arm scopes itself with IDFilter, which listIssues publishes no parameter for; the Offset "+
			"refusal this wire answers with instead is pinned by TestServedReaderRefusesTheFiltersTheWireCannotCarry")
	conformance.RunReaderOffsetSkipsTheRowsBeforeThePage(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListDefaultExclusionsAndTheirOverrides(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"every arm scopes itself with IDFilter, and two of them additionally set PinnedFlag/NoPinnedFlag, "+
			"which listIssues publishes no parameters for")
	conformance.RunReaderListDefaultExclusionsAndTheirOverrides(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListRejectsATypeOutsideTheWorkspaceVocabulary(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the built-in-type arm scopes itself with IDFilter; the vocabulary refusal itself is served (the "+
			"server validates against its own authoritative vocabulary, which is what heals L7 for this query)")
	conformance.RunReaderListRejectsATypeOutsideTheWorkspaceVocabulary(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListNaturalNumericIDSortTrimsAfterTheFetch(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the case scopes itself with IDFilter; the natural-numeric order it pins is exercised over this wire "+
			"by TestServedReaderListAppliesTheClientSideDisplayOrder")
	conformance.RunReaderListNaturalNumericIDSortTrimsAfterTheFetch(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListKeysetPositionResumesTheCreatedDescIDAscOrder(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the case scopes itself with IDFilter; the skip-forward walk it pins is exercised over this wire by "+
			"TestServedReaderListKeysetPositionSkipsForward, which is the same assertion under a label scope")
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
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the List arm scopes itself with IDFilter; the empty-page shape is pinned over this wire by "+
			"TestServedReaderListEmptyPageIsWellFormedUnderALabelScope")
	conformance.RunReaderListEmptyPageIsWellFormed(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderListQueryMatchesTheTitleOrTheID runs the search-text case as
// written: it scopes itself with a label, and Query rides listIssues' `q`
// parameter behind issues.list.search, which this in-process server serves.
func TestServedReaderListQueryMatchesTheTitleOrTheID(t *testing.T) {
	conformance.RunReaderListQueryMatchesTheTitleOrTheID(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListMaxRowsIsHonored(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the case scopes itself with IDFilter; the cap it pins is honored over this wire and exercised by "+
			"TestServedReaderListMaxRowsBoundsWireRowsFetched")
	conformance.RunReaderListMaxRowsIsHonored(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListSkipCountsDropsTheCardinalitiesAndNothingElse(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.SkipCounts", readParkBead,
		"the knob is DROPPED rather than refused (the wire hydrates the three cardinalities either way, and "+
			"every text rendering of `bd list` sets it, so refusing would kill the default listing over http); "+
			"the case additionally scopes itself with IDFilter")
	conformance.RunReaderListSkipCountsDropsTheCardinalitiesAndNothingElse(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListLimitBoundaryUnderASortTheDatabaseCanExpress(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the case scopes itself with IDFilter; the limit boundary under created order is pinned over this "+
			"wire by TestServedReaderListLimitBoundaryUnderALabelScope")
	conformance.RunReaderListLimitBoundaryUnderASortTheDatabaseCanExpress(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListReadyFlagCarriesTheAssigneeAndPriorityFilters(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.ReadyFlag", readParkBead,
		"listIssues does not publish the blocker-aware query")
	conformance.RunReaderListReadyFlagCarriesTheAssigneeAndPriorityFilters(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderListIncludeEphemeralMergesThePlanesIntoOneOrder is the plane
// contract client wave ga-mijra brought into range, and it parks for the same
// reason every List case here parks: it scopes itself with IDFilter, which
// listIssues publishes no parameter for.
//
// The behavior it asserts is pinned by
// TestServedReaderListIncludeEphemeralMergesThePlanesUnderALabelScope, which is
// the same fixture — alternating planes, every Limit cut, the keyset walk —
// with the id scope swapped for a label one, plus the plane-identity assertion
// the contract does not make.
func TestServedReaderListIncludeEphemeralMergesThePlanesIntoOneOrder(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the case scopes itself with ListRequest.IDFilter and listIssues publishes no id parameter; "+
			"the merged-plane order, the Limit cuts and the keyset walk are asserted under a label scope by "+
			"TestServedReaderListIncludeEphemeralMergesThePlanesUnderALabelScope")
	conformance.RunReaderListIncludeEphemeralMergesThePlanesIntoOneOrder(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// The FIVE remaining List parks, each named so the wiring lock counts the
// contract as accounted for rather than merely absent, and each deleted in one
// line the day its row retires.
//
// Three of them park on the id scope alone, which is this family's one cause.
// The other two would park even under a label scope, because their SUBJECT is a
// member the wire refuses — so each names that member instead.

func TestServedReaderListCountsAreBlocksOnlyWhereGetCountsEveryEdge(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the case scopes itself with IDFilter; the blocks-only rule it pins is exercised over this wire by "+
			"the cardinality assertions in TestServedReaderReadyPageIsThePrefixOfTheUnboundedAnswerCountsIncluded, "+
			"whose `family` row carries a parent-child and a relates-to edge and still reports zero dependencies")
	conformance.RunReaderListCountsAreBlocksOnlyWhereGetCountsEveryEdge(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListKeysetPositionNarrowsWithoutReplacingTheOtherPredicates(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the case scopes itself with IDFilter — its shadow rows share every timestamp with the scoped ones "+
			"and are excluded by the id set ALONE, which is the whole mechanism by which a widened position "+
			"is caught, so a label scope would not stand in for it")
	conformance.RunReaderListKeysetPositionNarrowsWithoutReplacingTheOtherPredicates(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListKeysetWalkOverAnOversizedGroupLosesNothingAndRepeatsNothing(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the case scopes itself with IDFilter; the keyset walk it pins is exercised over this wire by "+
			"TestServedReaderListKeysetPositionSkipsForward and by TestServedReaderListWalksMultiplePages, "+
			"both under a label scope")
	conformance.RunReaderListKeysetWalkOverAnOversizedGroupLosesNothingAndRepeatsNothing(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// The priority-order walk parks on the same row as the created-order one
// above, with a narrower stand-in beside it: the sort=priority pushdown is
// pinned under a label scope by TestServedReaderListFallsBackToTheWalkWithoutTheCapability,
// and resuming a priority keyset position — the pager's keysetFilter
// (list_walk.go) deciding priority first and the (created_at, id) pair only at
// the position's own priority — by TestServedReaderListPriorityKeysetPositionResumesThePriorityOrder.
// What neither covers is the WALK this case drives page after page over an
// oversized equal-key run, which stays unexercised over this wire until
// IDFilter (or a label-scoped rewrite) lets it run.
func TestServedReaderListPriorityKeysetWalkOverAnOversizedEqualKeyRunLosesNothingAndRepeatsNothing(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the case scopes itself with IDFilter, which listIssues publishes no parameter for, so its one-shot "+
			"read is refused before the walk begins; the sort=priority pushdown is pinned under a label scope by "+
			"TestServedReaderListFallsBackToTheWalkWithoutTheCapability, and one resumed priority keyset position by "+
			"TestServedReaderListPriorityKeysetPositionResumesThePriorityOrder")
	conformance.RunReaderListPriorityKeysetWalkOverAnOversizedEqualKeyRunLosesNothingAndRepeatsNothing(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderListMaxRowsBoundaryIsLimitPlusOffset names OFFSET rather than
// the id scope it also carries, because Offset is the one this case could not
// be rewritten around: the whole point is to drive the cap along the OFFSET
// axis with the limit held still, and listIssues publishes no offset parameter.
// The LIMIT axis of the same boundary runs over this wire in
// TestServedReaderListMaxRowsBoundsWireRowsFetched.
func TestServedReaderListMaxRowsBoundaryIsLimitPlusOffset(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.Offset", readParkBead,
		"the case moves the cap along the Offset axis, and listIssues publishes no offset parameter (it "+
			"scopes itself with IDFilter besides); the cap's Limit axis is driven over this wire by "+
			"TestServedReaderListMaxRowsBoundsWireRowsFetched")
	conformance.RunReaderListMaxRowsBoundaryIsLimitPlusOffset(t, t.Context(), servedReaderFixture(t, "rdr"))
}

// TestServedReaderListWispTypeNarrowsTheAdmittedPlaneRatherThanAdmittingIt
// names WISPTYPE for the same reason: the classification predicate IS the
// subject, and listIssues publishes no parameter for it, so no rescoping saves
// the case. The plane knob it composes against — IncludeEphemeral — is served,
// and its own half is asserted by
// TestServedReaderListIncludeEphemeralMergesThePlanesUnderALabelScope.
func TestServedReaderListWispTypeNarrowsTheAdmittedPlaneRatherThanAdmittingIt(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.WispType", readParkBead,
		"the wisp classification predicate is the case's subject and listIssues publishes no parameter for "+
			"it; the client refuses it rather than answering the whole admitted plane, which is the reading "+
			"the case exists to rule out")
	conformance.RunReaderListWispTypeNarrowsTheAdmittedPlaneRatherThanAdmittingIt(t, t.Context(), servedReaderFixture(t, "rdr"))
}

func TestServedReaderListStatusAcceptsACommaSeparatedORSet(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the case scopes itself with IDFilter; the OR set it pins is exercised over this wire by "+
			"TestServedReaderListStatusORSetUnderALabelScope, which drives the same three status shapes")
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

// TestServedReaderListBriefDropsTheFreeFormTextAndNothingElse parks for the
// reason every List case here parks — it scopes itself with IDFilter, and
// listIssues publishes no id parameter — NOT for anything about the projection.
//
// The projection half is asserted over this wire by the ready sibling below,
// which drives the identical assertReaderBriefRow through the same encoder and
// the same row lift, under a label scope instead of an id one.
func TestServedReaderListBriefDropsTheFreeFormTextAndNothingElse(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the case scopes itself with IDFilter; the projection it pins — the six heavy columns empty, the "+
			"identity fields and the three cardinalities intact, and IsLitePartial set — is asserted over this "+
			"wire by TestServedReaderReadyBriefDropsTheFreeFormTextAndNothingElse under a label scope")
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

func TestServedReaderDoesNotMutateTheCallerRequest(t *testing.T) {
	skipKnownDivergence(t, "E-ListRequest.IDFilter", readParkBead,
		"the List leg of the tripwire scopes itself with IDFilter; the promise is exercised over this wire "+
			"by TestServedReaderDoesNotMutateTheCallerRequestOverTheWire")
	conformance.RunReaderDoesNotMutateTheCallerRequest(t, t.Context(), servedReaderFixture(t, "rdr"))
}
