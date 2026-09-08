package db

import (
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueSearchUnionWindow() {
	s.Run("GoSideSortWithALimitAnswersTheGloballyCorrectPage", s.unionGoSideSortKeepsTheRightSubset)
	s.Run("TheCountsTwinAnswersTheSamePage", s.unionCountsGoSideSortKeepsTheRightSubset)
	s.Run("TheComposedQueryRunsWithBoundedLegs", s.unionComposedQueryWithBoundedLegs)
	s.Run("LegsCarryTheWindowUnderASortSQLCanExpress", s.unionLegsCarryTheWindow)
	s.Run("LegsStayUnboundedUnderAGoSideSort", s.unionLegsStayUnboundedUnderGoSideSort)
}

// seedTwoPlanes writes n durable rows and n ephemeral rows under one id prefix,
// INTERLEAVED by id so that neither plane holds a prefix of the id order: the
// durable rows take the even suffixes and the wisps the odd ones. A page of the
// first k ids therefore has to come from both planes, which is what makes a
// per-leg subset observable — a leg that answered with an arbitrary k rows, or
// a union that took its k from whichever leg the engine emitted first, produces
// a visibly different set rather than the same one in a different order.
func (s *testSuite) seedTwoPlanes(prefix string, n int) []string {
	r := s.issueRepo()
	ids := make([]string, 0, 2*n)
	for i := 0; i < 2*n; i++ {
		id := fmt.Sprintf("%s-%03d", prefix, i)
		issue := newTestIssue(id, fmt.Sprintf("row %d", i))
		opts := domain.InsertIssueOpts{}
		if i%2 == 1 {
			issue.Ephemeral = true
			opts.UseWispsTable = true
		}
		s.Require().NoError(r.Insert(s.Ctx(), issue, "tester", opts))
		ids = append(ids, id)
	}
	return ids
}

// unionGoSideSortKeepsTheRightSubset is the F5 case. SortBy "id" is the one key
// sqlbuild renders no ORDER BY for, so before this fix the union query was
// `SELECT id, src FROM (leg UNION ALL leg) merged  LIMIT k` — a LIMIT with no
// ordering anywhere in the statement, cutting an arbitrary k rows out of a
// concatenation. The Go-side sort then put those arbitrary rows in order, which
// is why the failure reads as a correct-looking page: the ORDER is right and
// the MEMBERSHIP is wrong.
func (s *testSuite) unionGoSideSortKeepsTheRightSubset() {
	const prefix = "bd-uw-gos"
	ids := s.seedTwoPlanes(prefix, 12)
	r := s.issueRepo()

	const limit = 5
	page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: prefix + "-", SortBy: "id", Limit: limit})
	s.Require().NoError(err)

	// The ids are zero-padded, so byte order and natural order agree and the
	// answer is unambiguous: the first `limit` of the seeded sequence, which
	// spans both planes.
	want := ids[:limit]
	s.Equal(want, idsFrom(page),
		"a limited page under a Go-side sort must be the globally first rows, not an arbitrary subset of the concatenation")
	s.True(page.HasMore)
}

// unionCountsGoSideSortKeepsTheRightSubset is the same case on the counts twin,
// and that is the twin the live callers reach. workapi.BuildQueryPlan pushes
// SortBy and Limit straight onto the filter for any filter-expressible query —
// no SQLLimit guard, and DefaultQueryLimit means a limit is always set — so
// `bd query '<expr>' --sort id` and GET /v0/beads/issues/query?sort=id both
// arrived here with SortBy "id" and a bound. Its own comment says the pushdown
// makes "the page the first N in the requested order on every backend", which
// was true of every key except the one IsGoSideSort names.
func (s *testSuite) unionCountsGoSideSortKeepsTheRightSubset() {
	const prefix = "bd-uw-cgos"
	ids := s.seedTwoPlanes(prefix, 12)
	r := s.issueRepo()

	const limit = 5
	page, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: prefix + "-", SortBy: "id", Limit: limit})
	s.Require().NoError(err)

	got := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		got = append(got, item.Issue.ID)
	}
	s.Equal(ids[:limit], got,
		"the counts twin bounds the same page the plain twin does")
	s.True(page.HasMore)
}

// unionComposedQueryWithBoundedLegs runs the WHOLE query — both twins, with and
// without a text term, ordered and unordered, bounded and not — rather than a
// leg on its own.
//
// It exists because the leg probe below does not cover this and cannot: it
// executes each subquery standalone, where `... ORDER BY x LIMIT n` is
// perfectly legal. Composed into a bare `leg UNION ALL leg` the same string is
// a SYNTAX ERROR, the engine reading the trailing clause as the union's own,
// and `bd search` failed outright until the legs were parenthesized. A probe
// that measures the part is not evidence about the whole.
func (s *testSuite) unionComposedQueryWithBoundedLegs() {
	const prefix = "bd-uw-cmp"
	s.seedTwoPlanes(prefix, 6)
	r := s.issueRepo()

	for _, tc := range []struct {
		what   string
		query  string
		filter types.IssueFilter
		want   int
	}{
		{"ordered and bounded", "", types.IssueFilter{IDPrefix: prefix + "-", SortBy: "created", Limit: 4}, 4},
		{"ordered, bounded, with an offset", "", types.IssueFilter{IDPrefix: prefix + "-", SortBy: "priority", Limit: 3, Offset: 2}, 3},
		{"ordered and bounded with a text term", "row", types.IssueFilter{IDPrefix: prefix + "-", SortBy: "updated", Limit: 4}, 4},
		{"the default order, bounded", "", types.IssueFilter{IDPrefix: prefix + "-", Limit: 5}, 5},
		{"ordered and unbounded", "", types.IssueFilter{IDPrefix: prefix + "-", SortBy: "created"}, 12},
		{"a Go-side sort, bounded", "", types.IssueFilter{IDPrefix: prefix + "-", SortBy: "id", Limit: 4}, 4},
	} {
		s.Run(tc.what, func() {
			page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), tc.query, tc.filter)
			s.Require().NoError(err)
			s.Len(page.Items, tc.want)

			counts, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), tc.query, tc.filter)
			s.Require().NoError(err)
			s.Len(counts.Items, tc.want)
		})
	}
}

// unionLegsCarryTheWindow is the F4 measurement: a ROW-COUNT PROBE on the legs
// themselves. It builds the same subqueries searchUnion builds and executes
// each one on its own, so "the legs stop over-fetching" is a number rather than
// an assertion about SQL text.
func (s *testSuite) unionLegsCarryTheWindow() {
	const prefix = "bd-uw-leg"
	s.seedTwoPlanes(prefix, 20) // 20 durable + 20 wisps
	r := NewIssueSQLRepository(s.Runner()).(*issueSQLRepositoryImpl)

	filter := types.IssueFilter{IDPrefix: prefix + "-", SortBy: "created", Limit: 4}
	outerOrderBy := unionOrderBySQL(filter.SortBy, filter.SortDesc)
	s.Require().NotEmpty(outerOrderBy, "this case needs a sort SQL can express")
	window := searchWindowForFilter(filter)

	for _, leg := range []struct {
		name   string
		tables filterTables
		tag    string
	}{
		{"issues", issuesFilterTables, "i"},
		{"wisps", wispsFilterTables, "w"},
	} {
		sub, args, err := r.buildUnionSubquery("", filter, leg.tables, leg.tag, legWindowSQL(outerOrderBy, window))
		s.Require().NoError(err)

		rows, err := s.db.QueryContext(s.Ctx(), sub, args...)
		s.Require().NoError(err)
		scanned := 0
		for rows.Next() {
			scanned++
		}
		s.Require().NoError(rows.Err())
		s.Require().NoError(rows.Close())

		// 20 rows match in each plane. The outer query can consume at most
		// window.legLimit of them, so a leg that returns more has done work no
		// caller can observe.
		s.Positive(window.legLimit, "a bounded search must bound its legs")
		s.LessOrEqual(scanned, window.legLimit,
			"%s leg returned %d rows for a window of %d", leg.name, scanned, window.legLimit)
		s.Equal(window.legLimit, scanned,
			"%s leg should fill the window exactly: 20 rows match", leg.name)
	}
}

// unionLegsStayUnboundedUnderGoSideSort pins the other half of the same rule. A
// LIMIT on a leg is only sound when the leg is ORDERED, so the sort SQL cannot
// express must not get one — a bounded leg with no ORDER BY is precisely the F5
// bug, moved one level down and made harder to see.
func (s *testSuite) unionLegsStayUnboundedUnderGoSideSort() {
	filter := types.IssueFilter{IDPrefix: "bd-uw-none-", SortBy: "id", Limit: 4}
	outerOrderBy := unionOrderBySQL(filter.SortBy, filter.SortDesc)
	s.Empty(outerOrderBy, "sortBy=id is the Go-side sort: SQL renders no ORDER BY for it")

	window := searchWindowForFilter(filter)
	s.Empty(window.sql, "a Go-side sort takes no SQL window: the page is cut where the requested order first exists")
	s.Zero(window.legLimit, "and with no outer window there is nothing to push into the legs")
	s.Empty(legWindowSQL(outerOrderBy, window))
	s.Equal(4, window.limit, "the Go half still trims to the caller's page")
}
