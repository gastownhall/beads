//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_querier_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/issueops"
)

// The Querier contract, run through the composition (design D8 row 5).
//
// Every case here runs but one: a QueryRequest carries a SENTENCE plus four
// page and order knobs, and queryIssues publishes a parameter for all of them
// except Offset, so there is no SCOPING field for the wire to refuse. That is
// close to the shape of a role the wire covers completely, and it is worth
// saying out loud beside the Reader wiring next door, where a refused scoping
// field is the rule rather than the exception.

func servedQuerierFixture(t *testing.T, name string) conformance.QuerierFixture {
	t.Helper()
	c := composition(t)
	f := c.fixture(t)
	querier := bindRole(t, f, func(s *Store) (issueops.Querier, error) { return s.Querier() })
	return conformance.QuerierFixture{
		IssuePrefix:  servedIssuePrefix + "-" + name,
		Querier:      querier,
		CreateIssue:  c.seedIssue,
		CountHistory: c.countHistory,
	}
}

func TestServedQuerierDisjunctionAnswersEveryMatch(t *testing.T) {
	conformance.RunQuerierDisjunctionAnswersEveryMatch(t, t.Context(), servedQuerierFixture(t, "qry"))
}

func TestServedQuerierPageIsAPrefixAndHasMoreIsExact(t *testing.T) {
	conformance.RunQuerierPageIsAPrefixAndHasMoreIsExact(t, t.Context(), servedQuerierFixture(t, "qry"))
}

func TestServedQuerierSortBoundsThePageInOrder(t *testing.T) {
	conformance.RunQuerierSortBoundsThePageInOrder(t, t.Context(), servedQuerierFixture(t, "qry"))
}

// The three ORDER cases beside it, and they are where this wire earns its
// keep: `sort` is a published parameter on queryIssues, so the order is the
// SERVER's own SQL rather than the page-to-exhaustion Go mirror `bd list` falls
// back to (L2). Each pins an ordering rule a re-implementation would get subtly
// wrong — the case fold before the cut, the unclosed rows at the far end of a
// closed sort, and the id tie-break in both directions.
func TestServedQuerierSortByTitleFoldsCaseBeforeItCutsThePage(t *testing.T) {
	conformance.RunQuerierSortByTitleFoldsCaseBeforeItCutsThePage(t, t.Context(), servedQuerierFixture(t, "qry"))
}

func TestServedQuerierSortByClosedPutsTheUnclosedRowsAtTheFarEnd(t *testing.T) {
	conformance.RunQuerierSortByClosedPutsTheUnclosedRowsAtTheFarEnd(t, t.Context(), servedQuerierFixture(t, "qry"))
}

func TestServedQuerierSortTieBreaksByIDInBothDirections(t *testing.T) {
	conformance.RunQuerierSortTieBreaksByIDInBothDirections(t, t.Context(), servedQuerierFixture(t, "qry"))
}

func TestServedQuerierSortSeesTheWholeMatchingSet(t *testing.T) {
	conformance.RunQuerierSortSeesTheWholeMatchingSet(t, t.Context(), servedQuerierFixture(t, "qry"))
}

func TestServedQuerierHidesClosedUnlessTheExpressionOrTheFlagSaysOtherwise(t *testing.T) {
	conformance.RunQuerierHidesClosedUnlessTheExpressionOrTheFlagSaysOtherwise(t, t.Context(), servedQuerierFixture(t, "qry"))
}

func TestServedQuerierRefusesAMalformedRequest(t *testing.T) {
	conformance.RunQuerierRefusesAMalformedRequest(t, t.Context(), servedQuerierFixture(t, "qry"))
}

// TestServedQuerierOffsetSkipsMatches is the one parked Querier case, and it
// became one upstream rather than here: the contract used to accept "honored OR
// refused with a typed *ErrUnsupported" and now asserts the skip outright, on
// the ruling that a contract ratifying a split is not pinning semantics.
//
// This wire refuses. queryIssues publishes no offset parameter — deliberately,
// because the two database sources a bd serve can be built on disagree about
// whether they can honor one — so the client has nothing to send and refuses
// rather than answering an unskipped page the caller would read as skipped.
// TestTheRolesOwnPageRefusalsAreValidationNotCapability is where that refusal
// is asserted — as a typed *ErrUnsupported rather than a validation error, which
// is the distinction that makes it this backend's statement rather than the
// caller's mistake — so the park stands in for nothing.
func TestServedQuerierOffsetSkipsMatches(t *testing.T) {
	skipKnownDivergence(t, "E-QueryRequest.Offset", readParkBead,
		"queryIssues publishes no offset parameter and the client refuses a non-zero Offset; the case now "+
			"asserts the skip itself, which this wire cannot express")
	conformance.RunQuerierOffsetSkipsMatches(t, t.Context(), servedQuerierFixture(t, "qry"))
}

func TestServedQuerierEmptyMatchIsAWellFormedPage(t *testing.T) {
	conformance.RunQuerierEmptyMatchIsAWellFormedPage(t, t.Context(), servedQuerierFixture(t, "qry"))
}

// TestServedQuerierWritesNothing reads the reference store's own commit log for
// its observable. The client cannot see history — v0 publishes no history
// surface — so a fixture that took CountHistory off the SUBJECT would skip, and
// a skipped case is exactly what this wiring is not allowed to produce.
func TestServedQuerierWritesNothing(t *testing.T) {
	conformance.RunQuerierWritesNothing(t, t.Context(), servedQuerierFixture(t, "qry"))
}

func TestServedQuerierDoesNotMutateTheCallerRequest(t *testing.T) {
	conformance.RunQuerierDoesNotMutateTheCallerRequest(t, t.Context(), servedQuerierFixture(t, "qry"))
}
