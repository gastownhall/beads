package conformance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

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
// genuinely separate vote. They diverge only where the uow seam renders OFFSET
// and reports has-more natively, which is why the Offset case is written
// comparatively.
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

// RunQuerierOffsetIsHonoredOrRefused pins the one thing every implementation
// owes on QueryRequest.Offset (issueops/querier.go:65-83): a non-zero Offset is
// either HONORED or REFUSED with a typed *ErrUnsupported, and never SILENTLY
// IGNORED.
//
// It is deliberately weaker than "Offset skips N rows", for the same reason
// RunReaderOffsetIsHonoredOrRefused is: the two bodies disagree by design,
// because one seam renders OFFSET and the other does not. Which body does which
// is asserted at the wirings, not here.
//
// BOTH SHAPES OF EXPRESSION ARE DRIVEN, which is what this case adds over the
// reader's: a predicate query's offset is applied in Go, after the predicate, a
// different code path from the filter-expressible one.
//
// EVERY REQUEST HERE CARRIES A BOUNDED LIMIT: on the unit-of-work seam an
// UNLIMITED request with a non-zero Offset renders SQL the Dolt engine answers
// with a recovered panic ("makeslice: cap out of range" out of topRowsIter)
// instead of rows. That is a storage-seam bug that predates this role, not a
// contract question, so this case asks the question it can answer rather than
// pinning a crash as behavior.
func RunQuerierOffsetIsHonoredOrRefused(t *testing.T, ctx context.Context, fixture QuerierFixture) {
	t.Helper()
	scope := querierLabel(fixture, "offset")
	for _, tag := range []string{"a", "b", "c"} {
		seedQuerierIssue(t, ctx, fixture, querierIssue(querierID(fixture, "offset", tag), types.TypeBug, 1, scope))
	}

	for _, test := range []struct {
		what       string
		expression string
	}{
		{"filter-expressible", fmt.Sprintf("type=bug AND label=%s", scope)},
		{"predicate", fmt.Sprintf("(type=bug OR type=epic) AND label=%s", scope)},
	} {
		t.Run(test.what, func(t *testing.T) {
			request := publicops.QueryRequest{Expression: test.expression, Limit: querierLimit(10)}
			// Offset 0 is served everywhere; it is the baseline the paged call
			// has to differ from.
			unpaged := querierIDs(t, ctx, fixture, request)
			if len(unpaged) != 3 {
				t.Fatalf("Offset 0 returned %v, want the three seeded rows", unpaged)
			}

			paged := request
			paged.Offset = 1
			page, err := fixture.Querier.Query(ctx, paged)
			if err != nil {
				var unsupported *publicops.ErrUnsupported
				if !errors.As(err, &unsupported) {
					t.Fatalf("Offset 1 refused with %v; a refusal has to be a typed *ErrUnsupported a caller can classify", err)
				}
				if unsupported.Op == "" || unsupported.Backend == "" {
					t.Errorf("Offset 1 refused with Op=%q Backend=%q; a refusal naming neither the operation nor the backend leaves the caller nowhere to go",
						unsupported.Op, unsupported.Backend)
				}
				return
			}
			got := querierPageIDs(page)
			if slices.Equal(got, unpaged) {
				t.Fatalf("Offset 1 returned the same page as Offset 0 (%v) and no error: the offset was silently ignored", got)
			}
			// Honored means it skipped MATCHES: two of the three, still in the
			// unpaged order, never a page cut from rejected candidates.
			if want := unpaged[1:]; !slices.Equal(got, want) {
				t.Errorf("Offset 1 = %v, want %v — the tail of the same answer", got, want)
			}
		})
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
