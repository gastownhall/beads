package conformance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file holds the semantic contract every implementation of
// publicops.Querier must satisfy. Each case asserts what issueops/querier.go
// PROMISES, cited by line, rather than what any one backend happens to do; a
// backend that disagrees is parked at its own wiring site with
// skipKnownDivergence so the case still runs on the ones that agree.
//
// TWO BODIES BEHIND THREE WIRINGS: dolt and embeddeddolt share
// internal/workapi/storequerier, and the unit-of-work provider is the second,
// genuinely separate vote. What is left of their difference is mechanism, not
// answer: the uow seam renders OFFSET and reports has-more natively, the other
// reaches past the skipped rows and lets an over-fetched row speak. Every case
// here is written to the answer, which is why none of them names a backend.
//
// WHAT THESE CASES DO NOT PROVE: the max(3*Limit, 100) over-fetch window both
// front doors used to apply, which takes MORE THAN A HUNDRED candidate rows to
// observe. That is pinned by TestBuildQueryPlanLeavesAPredicateQueryUNBOUNDED
// in internal/workapi and by one end-to-end run in internal/storage/dolt. What
// the cases here pin is the PROMISE the window broke: a page is a prefix of the
// complete matching set and has-more is exact.
//
// EVERY CASE IS SCOPED BY A LABEL asked for inside the EXPRESSION: a query
// request has no filter fields, so a case that did not name its own rows would
// answer about every row every other case seeded.

// QuerierFixture supplies adapter-specific storage access for the boolean-query
// assertions. Every field is named and typed exactly like the per-backend
// roleFixtureKit hook it is filled from.
type QuerierFixture struct {
	// IssuePrefix namespaces the ids each assertion seeds, so several of them
	// can share one database.
	IssuePrefix string
	// Querier is the surface under test.
	Querier publicops.Querier
	// CreateIssue seeds a durable issue in the issues plane.
	CreateIssue func(context.Context, *types.Issue, string) error
	// CountHistory reports how many history entries the fixture's branch has.
	// A nil hook means "this backend cannot observe history", and the case that
	// needs it SKIPS with that reason rather than passing quietly.
	CountHistory func(context.Context) (int, error)
}

// RunQuerierDisjunctionAnswersEveryMatch pins the capability that makes this a
// role at all (issueops/querier.go:99-107): an OR, which no conjunction of
// ListRequest fields expresses, answers with the union of both arms and nothing
// else. The NOT arm is not redundant — its complement includes rows no base
// filter narrowed, so an implementation leaning on the base filters answers it
// wrongly.
func RunQuerierDisjunctionAnswersEveryMatch(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	scope := querierLabel(fixture, "or")
	bug := querierIssue(querierID(fixture, "or", "bug"), types.TypeBug, 1, scope)
	chore := querierIssue(querierID(fixture, "or", "chore"), types.TypeChore, 2, scope)
	task := querierIssue(querierID(fixture, "or", "task"), types.TypeTask, 3, scope)
	for _, issue := range []*types.Issue{bug, chore, task} {
		seedQuerierIssue(t, ctx, fixture, issue)
	}

	got := querierIDs(t, ctx, fixture, publicops.QueryRequest{
		Expression: fmt.Sprintf("(type=bug OR type=chore) AND label=%s", scope),
	})
	want := []string{bug.ID, chore.ID}
	assertQuerierAnswered(t, got, want, "an OR must answer with the union of both arms and no more")

	got = querierIDs(t, ctx, fixture, publicops.QueryRequest{
		Expression: fmt.Sprintf("NOT type=task AND label=%s", scope),
	})
	assertQuerierAnswered(t, got, want, "a NOT must answer with the complement inside the scope")
}

// RunQuerierPageIsAPrefixAndHasMoreIsExact pins the promise the over-fetch
// broke (issueops/querier.go:116-137): the page is the first Limit MATCHES of
// the complete matching set, and HasMore is true exactly when the page is
// shorter than that set.
//
// The limit is walked across the boundary — one below the match count, exactly
// on it, and one above. An implementation that reported has-more from the row
// count the DATABASE returned rather than from the count of MATCHES passes the
// middle case and fails the other two.
func RunQuerierPageIsAPrefixAndHasMoreIsExact(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	scope := querierLabel(fixture, "page")
	// Four matching rows and one that does not, so the matching set is a STRICT
	// subset of what the base filters admit: a limit applied to the wrong one
	// of those two answers a different page.
	for i, tag := range []string{"a", "b", "c", "d"} {
		seedQuerierIssue(t, ctx, fixture, querierIssue(querierID(fixture, "page", tag), types.TypeBug, i, scope))
	}
	seedQuerierIssue(t, ctx, fixture, querierIssue(querierID(fixture, "page", "miss"), types.TypeTask, 0, scope))

	expression := fmt.Sprintf("(type=bug OR type=epic) AND label=%s", scope)
	whole := querierPage(t, ctx, fixture, publicops.QueryRequest{Expression: expression, Limit: querierLimit(0)})
	if len(whole.Items) != 4 {
		t.Fatalf("unlimited query returned %v, want the four matching rows; the rest of this case would then bound the wrong set",
			querierPageIDs(whole))
	}
	if whole.HasMore {
		t.Errorf("unlimited query reported has_more; nothing was hidden from it")
	}
	complete := querierPageIDs(whole)

	for _, test := range []struct {
		limit       int
		wantItems   int
		wantHasMore bool
	}{
		{3, 3, true},
		{4, 4, false},
		{5, 4, false},
	} {
		page := querierPage(t, ctx, fixture, publicops.QueryRequest{Expression: expression, Limit: querierLimit(test.limit)})
		ids := querierPageIDs(page)
		if len(ids) != test.wantItems {
			t.Errorf("Limit=%d returned %d rows (%v), want %d", test.limit, len(ids), ids, test.wantItems)
		}
		if page.HasMore != test.wantHasMore {
			t.Errorf("Limit=%d reported has_more=%v, want %v: the verdict is about the MATCHING set, not the rows the database returned",
				test.limit, page.HasMore, test.wantHasMore)
		}
		if !slices.Equal(ids, complete[:min(len(ids), len(complete))]) {
			t.Errorf("Limit=%d returned %v, want a prefix of the complete answer %v", test.limit, ids, complete)
		}
	}
}

// RunQuerierSortBoundsThePageInOrder pins the OTHER half of the display-order
// promise, on the shape the case below cannot reach.
//
// A FILTER-EXPRESSIBLE expression is bounded by the database, so the page is
// the first Limit rows IN THE REQUESTED ORDER — not the first rows the engine
// happened to return, re-sorted afterwards.
//
// It is the quadrant no case reached: the sort case below drives only
// predicate (OR) expressions, and nothing combined a filter-expressible
// expression with a sort and a Limit smaller than the match count. The two
// bodies disagreed there — the store body over-fetches one row as a has-more
// probe and sorted that probe INTO the page, the unit-of-work body trims to
// Limit natively and never sees it.
//
// THE FIXTURE IS BUILT SO STORAGE ORDER AND SORT ORDER DISAGREE, which is what
// makes the case able to fail at all. Four rows match, Limit is two, and the
// two rows that should win on `title` carry the WORST priorities — so the default
// engine order puts them last, outside the three rows an over-fetch would see.
// A body that bounds in storage order and sorts afterwards returns neither of
// them.
func RunQuerierSortBoundsThePageInOrder(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	scope := querierLabel(fixture, "pageorder")
	// Title order is a < b < y < z (querierIssue titles the row with its id);
	// priority order is the reverse. `title` is SQL-expressible, unlike `id`,
	// which sqlbuild routes to a Go-side sort where no push-down can apply.
	first := querierIssue(querierID(fixture, "pageorder", "a"), types.TypeBug, 4, scope)
	second := querierIssue(querierID(fixture, "pageorder", "b"), types.TypeBug, 3, scope)
	third := querierIssue(querierID(fixture, "pageorder", "y"), types.TypeBug, 1, scope)
	fourth := querierIssue(querierID(fixture, "pageorder", "z"), types.TypeBug, 0, scope)
	for _, issue := range []*types.Issue{first, second, third, fourth} {
		seedQuerierIssue(t, ctx, fixture, issue)
	}

	// A conjunction the filter vocabulary expresses exactly, so the database
	// carries the page rather than the evaluator.
	expression := fmt.Sprintf("type=bug AND label=%s", scope)

	page, err := fixture.Querier.Query(ctx, publicops.QueryRequest{
		Expression: expression, SortBy: "title", Limit: querierLimit(2),
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if want := []string{first.ID, second.ID}; !slices.Equal(querierPageIDs(page), want) {
		t.Errorf("page = %v, want %v: a bounded page is the first Limit rows IN ORDER, not the rows "+
			"the engine returned in its own order and then sorted", querierPageIDs(page), want)
	}
	if !page.HasMore {
		t.Error("HasMore = false with four matches and a limit of two")
	}

	reversed, err := fixture.Querier.Query(ctx, publicops.QueryRequest{
		Expression: expression, SortBy: "title", Reverse: true, Limit: querierLimit(2),
	})
	if err != nil {
		t.Fatalf("Query reversed: %v", err)
	}
	if want := []string{fourth.ID, third.ID}; !slices.Equal(querierPageIDs(reversed), want) {
		t.Errorf("reversed page = %v, want %v", querierPageIDs(reversed), want)
	}
}

// RunQuerierSortByTitleFoldsCaseBeforeItCutsThePage pins the title order's
// COLLATION, which the case above is structurally unable to observe.
//
// THE DEFECT IS IN THE FIXTURE, not in the assertion. The case above titles
// every row with its own id, so every title is lower-case: byte order, folded
// order and linguistic order are the same sequence over `a b y z`, and an
// engine whose text comparison is byte-wise satisfies it exactly. That is the
// ordering-fixture trap ADDING_AN_ISSUEOPS_ROLE.md names by name. The five
// titles here are chosen so the three orders disagree — byte order is
// APPLE2 < Apple < Zebra < apple < banana, folded order is
// apple = Apple < APPLE2 < banana < Zebra — so which rows survive a cut of two
// is a different answer under each.
//
// THE CUT IS WHERE IT IS OBSERVABLE, and the case has both arms because the two
// arms fail on different legs. A bounded page is the rows the DATABASE kept, so
// a collation break changes the page on every leg. An unbounded answer is
// re-sorted after the fact by the shared Go comparator, which folds — and the
// one thing that comparator cannot restore is the order INSIDE a folded tie,
// which it reports as equal and a stable sort therefore leaves in the order the
// query returned. So the unbounded arm pins the tie leg, and on the unit-of-work
// leg — whose page order comes straight out of the UNION's ORDER BY — that is
// the collation's own answer.
//
// WHAT THIS FIXTURE CANNOT SEE: the store legs' unbounded tie. Those two
// backends re-order the merged rows with sqlbuild.Less before the page is cut,
// and its tie-break is id ASC in Go, so the unbounded arm cannot fail there
// however the engine collates. The bounded arm is the one that speaks on all
// three legs.
//
// The two winners carry the WORST priorities, so the default engine order puts
// them last: a body that bounded in storage order and sorted afterwards returns
// neither of them, which is the property this case inherits from the one above.
func RunQuerierSortByTitleFoldsCaseBeforeItCutsThePage(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	scope := querierLabel(fixture, "titlefold")
	// The ids ascend with the tags, so the two rows that fold to the same title
	// pin the id-ASC tie leg in a known direction.
	lower := querierTitledIssue(querierID(fixture, "titlefold", "1"), "apple", 3, scope)
	upper := querierTitledIssue(querierID(fixture, "titlefold", "2"), "Apple", 3, scope)
	shouted := querierTitledIssue(querierID(fixture, "titlefold", "3"), "APPLE2", 2, scope)
	fruit := querierTitledIssue(querierID(fixture, "titlefold", "4"), "banana", 1, scope)
	animal := querierTitledIssue(querierID(fixture, "titlefold", "5"), "Zebra", 0, scope)
	for _, issue := range []*types.Issue{lower, upper, shouted, fruit, animal} {
		seedQuerierIssue(t, ctx, fixture, issue)
	}

	// A conjunction the filter vocabulary expresses exactly, so the database
	// carries both the order and the bound.
	expression := fmt.Sprintf("type=bug AND label=%s", scope)
	want := []string{lower.ID, upper.ID, shouted.ID, fruit.ID, animal.ID}

	whole := querierIDs(t, ctx, fixture, publicops.QueryRequest{
		Expression: expression, SortBy: "title", Limit: querierLimit(0),
	})
	if !slices.Equal(whole, want) {
		t.Errorf("title order = %v, want %v: titles compare case-folded, and two titles that fold alike break by id ASC",
			whole, want)
	}

	page := querierIDs(t, ctx, fixture, publicops.QueryRequest{
		Expression: expression, SortBy: "title", Limit: querierLimit(2),
	})
	if !slices.Equal(page, want[:2]) {
		t.Errorf("bounded page = %v, want %v: the cut keeps the first rows of the CASE-FOLDED order, so a byte-wise "+
			"collation hands back a different page and no error", page, want[:2])
	}
}

// RunQuerierSortByClosedPutsTheUnclosedRowsAtTheFarEnd pins the nullable sort
// key's placement — the promise sqlbuild leads its clause with an explicit
// (col IS NULL) term to keep: a row with no value for the key sorts LAST under
// the key's own direction and FIRST when the direction is reversed, whatever
// the driver's native NULL ordering happens to be.
//
// No contract case drove the closed sort at all before this one, in either
// direction.
//
// EVERY ARM IS BOUNDED, and that is the whole design of the case. Unbounded,
// the answer is re-sorted by the shared Go comparator, which has its own
// nil-handling — so an unbounded assertion is one body checking itself and
// passes with the SQL term deleted or inverted. Under a cut of two it is the
// DATABASE that decides which rows reach the epilogue at all, and no Go
// comparator can put back a row the query left behind. Each arm therefore
// carries a limit smaller than the match count, and the unbounded reads are
// kept only as the baseline the pages must be prefixes of.
//
// WHAT THIS FIXTURE CANNOT SEE: which encoding "no value" takes. closed_at is
// genuinely NULL on an open row, so this key exercises the NULL term; the
// assignee key does not, because an unassigned row's column holds the empty
// string — that key's placement is ordinary string order and is pinned in
// RunQuerierSortTieBreaksByIDInBothDirections instead. Nor does it see the
// closed-row DEFAULT: IncludeClosed is set on every request here, so the
// conditional hiding is somebody else's case.
func RunQuerierSortByClosedPutsTheUnclosedRowsAtTheFarEnd(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	scope := querierLabel(fixture, "closedsort")
	older := querierWholeSecond(2021)
	newer := querierWholeSecond(2022)

	openFirst := querierTitledIssue(querierID(fixture, "closedsort", "1"), "open first", 1, scope)
	openSecond := querierTitledIssue(querierID(fixture, "closedsort", "2"), "open second", 1, scope)
	shutOld := querierTitledIssue(querierID(fixture, "closedsort", "3"), "shut old", 1, scope)
	shutOld.Status = types.StatusClosed
	shutOld.ClosedAt = &older
	shutNew := querierTitledIssue(querierID(fixture, "closedsort", "4"), "shut new", 1, scope)
	shutNew.Status = types.StatusClosed
	shutNew.ClosedAt = &newer
	for _, issue := range []*types.Issue{openFirst, openSecond, shutOld, shutNew} {
		seedQuerierIssue(t, ctx, fixture, issue)
	}

	expression := fmt.Sprintf("type=bug AND label=%s", scope)
	request := func(reverse bool, limit int) publicops.QueryRequest {
		return publicops.QueryRequest{
			Expression: expression, IncludeClosed: true,
			SortBy: "closed", Reverse: reverse, Limit: querierLimit(limit),
		}
	}

	// The key's own direction is newest-closed first, and the two rows that
	// were never closed follow every row that was.
	forward := []string{shutNew.ID, shutOld.ID, openFirst.ID, openSecond.ID}
	if got := querierIDs(t, ctx, fixture, request(false, 0)); !slices.Equal(got, forward) {
		t.Errorf("closed order = %v, want %v: newest close first, and the rows with no close last", got, forward)
	}
	if got := querierIDs(t, ctx, fixture, request(false, 2)); !slices.Equal(got, forward[:2]) {
		t.Errorf("bounded closed page = %v, want %v: an unclosed row must not displace a closed one from the page",
			got, forward[:2])
	}

	// Reversed, the unclosed rows lead — the placement flips with the
	// direction rather than staying pinned to one end of the answer.
	reversed := []string{openFirst.ID, openSecond.ID, shutOld.ID, shutNew.ID}
	if got := querierIDs(t, ctx, fixture, request(true, 0)); !slices.Equal(got, reversed) {
		t.Errorf("reversed closed order = %v, want %v: the rows with no close lead, then the oldest close",
			got, reversed)
	}
	if got := querierIDs(t, ctx, fixture, request(true, 2)); !slices.Equal(got, reversed[:2]) {
		t.Errorf("bounded reversed closed page = %v, want %v: reversing moves the unclosed rows INTO the page, "+
			"which is the half a driver's native NULL order gets wrong", got, reversed[:2])
	}
}

// RunQuerierSortTieBreaksByIDInBothDirections pins the other half of every
// non-default sort clause: rows the key cannot separate are ordered by id
// ASC, and reversing the request flips the KEY only — the tie-break never
// turns around.
//
// It is what makes a sorted page reproducible. Two rows updated in the same
// second, or sharing a status, are a tie at the key; if the tie-break flipped
// with the key, the same request answered twice around a reversal would visit
// the tied rows in two different orders and a caller paging through them would
// see rows move under it.
//
// THREE KEYS, BECAUSE THE TIE ARRIVES THREE DIFFERENT WAYS: a timestamp column
// (whole-second stamps that collide), an enumerated string column (a status
// two rows share), and a column that is EMPTY on every row here — the
// unassigned case, where the tied group is the entire answer and reversing the
// request must therefore change nothing at all.
//
// WHERE THE ANSWER COMES FROM DIFFERS BY LEG, which is why this is worth
// running on all three. The role's own epilogue reports a tie as equal and
// sorts stably, so it hands the decision back to whatever produced the rows:
// the unit-of-work leg's UNION ORDER BY, and the store legs' Go mirror of it
// (sqlbuild.Less). Two bodies, one promise.
//
// WHAT THIS FIXTURE CANNOT SEE: the direction of the KEY on a nullable column —
// no row here has a NULL sort key, so
// RunQuerierSortByClosedPutsTheUnclosedRowsAtTheFarEnd owns that — and any
// order below a page cut, since every read here is unbounded so that the tied
// group is visible whole.
func RunQuerierSortTieBreaksByIDInBothDirections(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	scope := querierLabel(fixture, "tiebreak")
	tied := querierWholeSecond(2020)
	fresh := querierWholeSecond(2022)

	first := querierTitledIssue(querierID(fixture, "tiebreak", "1"), "tied first", 1, scope)
	first.UpdatedAt = tied
	first.CreatedAt = tied
	second := querierTitledIssue(querierID(fixture, "tiebreak", "2"), "tied second", 1, scope)
	second.UpdatedAt = tied
	second.CreatedAt = tied
	shut := querierTitledIssue(querierID(fixture, "tiebreak", "3"), "tied closed", 1, scope)
	shut.Status = types.StatusClosed
	shut.UpdatedAt = tied
	shut.CreatedAt = tied
	moving := querierTitledIssue(querierID(fixture, "tiebreak", "4"), "moved lately", 1, scope)
	moving.Status = types.StatusInProgress
	moving.UpdatedAt = fresh
	moving.CreatedAt = fresh
	for _, issue := range []*types.Issue{first, second, shut, moving} {
		seedQuerierIssue(t, ctx, fixture, issue)
	}

	expression := fmt.Sprintf("type=bug AND label=%s", scope)
	answer := func(sortBy string, reverse bool) []string {
		return querierIDs(t, ctx, fixture, publicops.QueryRequest{
			Expression: expression, IncludeClosed: true,
			SortBy: sortBy, Reverse: reverse, Limit: querierLimit(0),
		})
	}

	for _, test := range []struct {
		name    string
		sortBy  string
		reverse bool
		want    []string
	}{
		// Most recently updated first; the three same-second rows are a tie.
		{"updated", "updated", false, []string{moving.ID, first.ID, second.ID, shut.ID}},
		// Reversed, the tied group moves to the front UNCHANGED.
		{"updated reversed", "updated", true, []string{first.ID, second.ID, shut.ID, moving.ID}},
		// closed < in_progress < open, and the two open rows tie.
		{"status", "status", false, []string{shut.ID, moving.ID, first.ID, second.ID}},
		{"status reversed", "status", true, []string{first.ID, second.ID, moving.ID, shut.ID}},
		// Nothing here is assigned, so the tie is the whole answer and
		// reversing it is a no-op.
		{"assignee", "assignee", false, []string{first.ID, second.ID, shut.ID, moving.ID}},
		{"assignee reversed", "assignee", true, []string{first.ID, second.ID, shut.ID, moving.ID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := answer(test.sortBy, test.reverse); !slices.Equal(got, test.want) {
				t.Errorf("sort by %q reverse=%v = %v, want %v: ties break by id ASC, and a reversal flips the key alone",
					test.sortBy, test.reverse, got, test.want)
			}
		})
	}
}

// RunQuerierSortSeesTheWholeMatchingSet pins the half of the display-order
// promise that only a predicate query can show (issueops/querier.go:45-53): a
// predicate query bounds nothing, so a one-row page under a sort is the best row
// in the whole matching set.
//
// The rows are seeded worst-priority-first so that "the first row the database
// returned" and "the highest-priority match" are different answers; an
// implementation that cut the page before ordering it returns the seeded-first
// row.
func RunQuerierSortSeesTheWholeMatchingSet(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	scope := querierLabel(fixture, "sort")
	low := querierIssue(querierID(fixture, "sort", "low"), types.TypeBug, 4, scope)
	high := querierIssue(querierID(fixture, "sort", "high"), types.TypeChore, 0, scope)
	mid := querierIssue(querierID(fixture, "sort", "mid"), types.TypeBug, 2, scope)
	for _, issue := range []*types.Issue{low, mid, high} {
		seedQuerierIssue(t, ctx, fixture, issue)
	}

	expression := fmt.Sprintf("(type=bug OR type=chore) AND label=%s", scope)
	ordered := querierIDs(t, ctx, fixture, publicops.QueryRequest{
		Expression: expression, SortBy: "priority", Limit: querierLimit(0),
	})
	if want := []string{high.ID, mid.ID, low.ID}; !slices.Equal(ordered, want) {
		t.Errorf("sorted query = %v, want %v", ordered, want)
	}

	reversed := querierIDs(t, ctx, fixture, publicops.QueryRequest{
		Expression: expression, SortBy: "priority", Reverse: true, Limit: querierLimit(0),
	})
	if want := []string{low.ID, mid.ID, high.ID}; !slices.Equal(reversed, want) {
		t.Errorf("reversed query = %v, want %v", reversed, want)
	}

	head := querierIDs(t, ctx, fixture, publicops.QueryRequest{
		Expression: expression, SortBy: "priority", Limit: querierLimit(1),
	})
	if want := []string{high.ID}; !slices.Equal(head, want) {
		t.Errorf("Limit=1 under a sort = %v, want %v: the order is applied to the whole matching set before the page is cut",
			head, want)
	}
}

// RunQuerierHidesClosedUnlessTheExpressionOrTheFlagSaysOtherwise pins the
// conditional default (issueops/querier.go:30-39). It is a CONTRACT clause
// rather than a CLI default because both front doors have always applied it.
func RunQuerierHidesClosedUnlessTheExpressionOrTheFlagSaysOtherwise(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	scope := querierLabel(fixture, "closed")
	open := querierIssue(querierID(fixture, "closed", "open"), types.TypeBug, 1, scope)
	shut := querierIssue(querierID(fixture, "closed", "shut"), types.TypeChore, 1, scope)
	shut.Status = types.StatusClosed
	for _, issue := range []*types.Issue{open, shut} {
		seedQuerierIssue(t, ctx, fixture, issue)
	}

	both := fmt.Sprintf("(type=bug OR type=chore) AND label=%s", scope)
	got := querierIDs(t, ctx, fixture, publicops.QueryRequest{Expression: both, Limit: querierLimit(0)})
	assertQuerierAnswered(t, got, []string{open.ID}, "a query with no opinion about status hides closed rows")

	got = querierIDs(t, ctx, fixture, publicops.QueryRequest{Expression: both, IncludeClosed: true, Limit: querierLimit(0)})
	assertQuerierAnswered(t, got, []string{open.ID, shut.ID}, "IncludeClosed admits them")

	got = querierIDs(t, ctx, fixture, publicops.QueryRequest{
		Expression: fmt.Sprintf("status=closed AND label=%s", scope), Limit: querierLimit(0),
	})
	assertQuerierAnswered(t, got, []string{shut.ID},
		"an expression that compares status keeps its own answer without IncludeClosed")
}

// RunQuerierRefusesAMalformedRequest pins every deterministic refusal
// (issueops/querier.go:23-27, 57-62, 65-91). Each is ErrValidation.
//
// A matching row is seeded first so every refusal runs against a store that
// WOULD have answered, rather than an empty result reported the hard way.
func RunQuerierRefusesAMalformedRequest(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	scope := querierLabel(fixture, "refuse")
	seedQuerierIssue(t, ctx, fixture, querierIssue(querierID(fixture, "refuse", "a"), types.TypeBug, 1, scope))
	valid := fmt.Sprintf("type=bug AND label=%s", scope)

	for _, test := range []struct {
		name string
		req  publicops.QueryRequest
	}{
		{"blank expression", publicops.QueryRequest{}},
		{"whitespace expression", publicops.QueryRequest{Expression: "  \t "}},
		{"unparseable expression", publicops.QueryRequest{Expression: "===invalid==="}},
		{"unknown field", publicops.QueryRequest{Expression: "nosuchfield=1"}},
		{"negative limit", publicops.QueryRequest{Expression: valid, Limit: querierLimit(-1)}},
		{"negative offset", publicops.QueryRequest{Expression: valid, Offset: -1}},
		{"offset under a display order", publicops.QueryRequest{Expression: valid, Offset: 1, SortBy: "priority"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			page, err := fixture.Querier.Query(ctx, test.req)
			if err == nil {
				t.Fatalf("Query(%+v) answered with %v instead of refusing", test.req, querierPageIDs(page))
			}
			if !errors.Is(err, publicops.ErrValidation) {
				t.Errorf("Query(%+v) error = %v, want ErrValidation", test.req, err)
			}
		})
	}
}

// RunQuerierOffsetSkipsMatches pins QueryRequest.Offset
// (issueops/querier.go:65-83): the page is the tail of the same answer, in the
// same order, on every implementation and for every shape of expression.
//
// It used to say "honored OR refused with a typed *ErrUnsupported", because one
// seam rendered OFFSET and the other did not and so refused. That disjunction
// described the split instead of pinning the promise; both bodies serve the
// offset now, one in SQL and one by reaching past the skipped rows.
//
// BOTH SHAPES OF EXPRESSION ARE DRIVEN, which is what this case adds over the
// reader's: a predicate query's offset is applied in Go, after the predicate, a
// different code path from the filter-expressible one — and the whole point of
// "skips MATCHES" is that a page cut from rejected candidates would be short.
//
// BOTH SHAPES OF PAGE BOUND ARE DRIVEN TOO. The bounded arm and the UNLIMITED
// arm reach different code: an unlimited request with an offset has no LIMIT to
// hang an OFFSET on, and the sentinel one seam used to render for it
// ("LIMIT 18446744073709551615 OFFSET k") came back as a recovered
// "makeslice: cap out of range" out of the Dolt engine's topRowsIter instead of
// rows. This case carried a bounded limit on every request to route around
// that. The seam skips those rows itself now, and the unlimited arm is what
// says so.
func RunQuerierOffsetSkipsMatches(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	scope := querierLabel(fixture, "offset")
	for _, tag := range []string{"a", "b", "c"} {
		seedQuerierIssue(t, ctx, fixture, querierIssue(querierID(fixture, "offset", tag), types.TypeBug, 1, scope))
	}

	for _, shape := range []struct {
		what       string
		expression string
	}{
		{"filter-expressible", fmt.Sprintf("type=bug AND label=%s", scope)},
		{"predicate", fmt.Sprintf("(type=bug OR type=epic) AND label=%s", scope)},
	} {
		for _, bound := range []struct {
			what  string
			limit int
		}{
			{"bounded", 10},
			{"unlimited", 0},
		} {
			t.Run(shape.what+"/"+bound.what, func(t *testing.T) {
				request := publicops.QueryRequest{Expression: shape.expression, Limit: querierLimit(bound.limit)}
				// Offset 0 is the baseline every paged call is a suffix of. An
				// unsorted page is in storage order, so it is read rather than
				// assumed.
				unpaged := querierIDs(t, ctx, fixture, request)
				if len(unpaged) != 3 {
					t.Fatalf("Offset 0 returned %v, want the three seeded rows", unpaged)
				}

				for offset := 1; offset <= len(unpaged); offset++ {
					paged := request
					paged.Offset = offset
					page, err := fixture.Querier.Query(ctx, paged)
					if err != nil {
						t.Errorf("Offset %d: %v", offset, err)
						continue
					}
					if page.Items == nil {
						t.Errorf("Offset %d returned a nil Items; an offset past the end is an empty page, not a null one", offset)
					}
					// It skipped MATCHES: the tail of the unpaged order, never
					// a page cut from rejected candidates.
					if got, want := querierPageIDs(page), unpaged[offset:]; !slices.Equal(got, want) {
						t.Errorf("Offset %d = %v, want %v — the tail of the same answer %v", offset, got, want, unpaged)
					}
				}
			})
		}
	}
}

// RunQuerierEmptyMatchIsAWellFormedPage pins the empty answer
// (issueops/querier.go:139-141): a nil error, an empty page that is not a nil
// slice, and no has-more. There is no ErrNotFound on this role — a question
// about a set has an answer even when the set is empty.
func RunQuerierEmptyMatchIsAWellFormedPage(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	scope := querierLabel(fixture, "empty")
	page, err := fixture.Querier.Query(ctx, publicops.QueryRequest{
		Expression: fmt.Sprintf("(type=bug OR type=epic) AND label=%s", scope),
	})
	if err != nil {
		t.Fatalf("Query over a scope with no rows: %v", err)
	}
	if page.Items == nil {
		t.Errorf("empty page carries a nil Items; a caller must not have to tell null from empty to learn that nothing matched")
	}
	if len(page.Items) != 0 {
		t.Errorf("empty scope answered with %v", querierPageIDs(page))
	}
	if page.HasMore {
		t.Errorf("empty page reported has_more")
	}
}

// RunQuerierWritesNothing pins that querying is a READ
// (issueops/querier.go:143-144): no history entry for a query that answers, and
// none for one that refuses either.
func RunQuerierWritesNothing(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	if fixture.CountHistory == nil {
		t.Skip("this backend cannot observe history, so 'a query writes nothing' cannot be asserted here")
	}
	scope := querierLabel(fixture, "write")
	seedQuerierIssue(t, ctx, fixture, querierIssue(querierID(fixture, "write", "a"), types.TypeBug, 1, scope))

	before, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory before: %v", err)
	}
	querierIDs(t, ctx, fixture, publicops.QueryRequest{
		Expression: fmt.Sprintf("(type=bug OR type=epic) AND label=%s", scope), Limit: querierLimit(0),
	})
	if _, err := fixture.Querier.Query(ctx, publicops.QueryRequest{Expression: "===invalid==="}); err == nil {
		t.Fatal("a malformed expression was accepted; this case then measures the wrong thing")
	}
	after, err := fixture.CountHistory(ctx)
	if err != nil {
		t.Fatalf("CountHistory after: %v", err)
	}
	if after != before {
		t.Errorf("history entries went %d -> %d across a query and a refusal; a read records nothing", before, after)
	}
}

// RunQuerierDoesNotMutateTheCallerRequest pins the no-mutation promise
// (issueops/querier.go:147-148) against a fully populated request. Limit is a
// POINTER, so an implementation that defaulted it by writing through that
// pointer would change the caller's own variable.
func RunQuerierDoesNotMutateTheCallerRequest(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	scope := querierLabel(fixture, "immutable")
	build := func() publicops.QueryRequest {
		return publicops.QueryRequest{
			Expression:    fmt.Sprintf("(type=bug OR type=chore) AND label=%s", scope),
			IncludeClosed: true,
			SortBy:        "priority",
			Reverse:       true,
			Limit:         querierLimit(2),
		}
	}
	request := build()
	want := build()

	if _, err := fixture.Querier.Query(ctx, request); err != nil {
		t.Fatalf("Query with a fully populated request: %v", err)
	}
	if !reflect.DeepEqual(request, want) {
		t.Errorf("Query mutated the caller's request:\n got %+v\nwant %+v", request, want)
	}
	if *request.Limit != *want.Limit {
		t.Errorf("Query wrote through the caller's Limit pointer: %d, want %d", *request.Limit, *want.Limit)
	}
}

func querierLabel(fixture QuerierFixture, name string) string {
	return fmt.Sprintf("%s-q-%s", fixture.IssuePrefix, name)
}

func querierID(fixture QuerierFixture, name, tag string) string {
	return fmt.Sprintf("%s-q%s-%s", fixture.IssuePrefix, name, tag)
}

func querierLimit(n int) *int { return &n }

func querierIssue(id string, issueType types.IssueType, priority int, labels ...string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     id,
		Status:    types.StatusOpen,
		Priority:  priority,
		IssueType: issueType,
		Labels:    labels,
	}
}

// querierTitledIssue is querierIssue for the ordering cases, which need a
// title that is not the row's own id: an id-titled fixture can only carry the
// lower-case, seed-ordered titles that make a collation break invisible.
func querierTitledIssue(id, title string, priority int, labels ...string) *types.Issue {
	issue := querierIssue(id, types.TypeBug, priority, labels...)
	issue.Title = title
	return issue
}

// querierWholeSecond is a fixed timestamp with no sub-second part, so a column
// stored at DATETIME(0) round-trips it exactly and two rows given the same one
// are a genuine tie rather than a truncation artifact.
func querierWholeSecond(year int) time.Time {
	return time.Date(year, time.June, 1, 12, 0, 0, 0, time.UTC)
}

func seedQuerierIssue(t *testing.T, ctx context.Context, fixture QuerierFixture, issue *types.Issue) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatalf("seed issue %s: %v", issue.ID, err)
	}
}

// querierPage runs one query and fails the case on an error.
func querierPage(t *testing.T, ctx context.Context, fixture QuerierFixture, request publicops.QueryRequest) publicops.IssuePage {
	t.Helper()
	page, err := fixture.Querier.Query(ctx, request)
	if err != nil {
		t.Fatalf("Query(%q, limit=%v, sort=%q): %v", request.Expression, request.Limit, request.SortBy, err)
	}
	return page
}

func querierIDs(t *testing.T, ctx context.Context, fixture QuerierFixture, request publicops.QueryRequest) []string {
	t.Helper()
	return querierPageIDs(querierPage(t, ctx, fixture, request))
}

func querierPageIDs(page publicops.IssuePage) []string {
	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		if item != nil && item.Issue != nil {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

// assertQuerierAnswered compares a page against the ids it must hold, ignoring
// order: the order of an unsorted page is storage order, not a promise.
func assertQuerierAnswered(t *testing.T, got, want []string, because string) {
	t.Helper()
	sortedGot := append([]string(nil), got...)
	sortedWant := append([]string(nil), want...)
	slices.Sort(sortedGot)
	slices.Sort(sortedWant)
	if !slices.Equal(sortedGot, sortedWant) {
		t.Errorf("query answered %v, want %v: %s", got, want, because)
	}
}
