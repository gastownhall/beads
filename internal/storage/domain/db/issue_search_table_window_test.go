package db

import (
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestIssueSearchPerTableWindow is the per-table (and counts-twin) copy of
// TestIssueSearchUnionWindow's Go-side-sort membership cases: #5587 fixed the
// union seam, and these pin the routes that never reach it — SkipWisps and an
// empty/missing wisps table — where searchTable/scanFilterIDs and the counts
// twin's finishSearchCountsPage trimmed to the limit with no sort anywhere
// (bd-69c1a), and a reversed id sort additionally kept the byte-FIRST rows
// because sqlbuild.Less ignored sortDesc for "id" (bd-jao3t).
//
// Subtest ORDER is load-bearing: the durable-only cases run first, while the
// wisps table is still empty, so the wisps-empty probe route is genuinely
// exercised; the union case seeds wisps and therefore runs last.
func (s *testSuite) TestIssueSearchPerTableWindow() {
	s.Run("GoSideSortWithALimitAnswersTheGloballyCorrectPage", s.perTableGoSideSortKeepsTheRightSubset)
	s.Run("AReversedGoSideSortAnswersTheByteLastRows", s.perTableGoSideSortDescKeepsTheByteLastRows)
	s.Run("EveryPerTableRouteAnswersOnePage", s.perTableRoutesAnswerOnePage)
	s.Run("TheUnionAnswersAReversedPageToo", s.unionGoSideSortDescKeepsTheByteLastRows)
}

// seedDurableRows writes n durable rows under one id prefix. Unlike
// seedTwoPlanes it keeps everything on the issues table: the per-table seams
// are the routes a search takes when the wisps plane is out of the picture
// (SkipWisps, or an empty/missing wisps table), and an empty wisps table is
// part of the route condition the callers below want to hold.
func (s *testSuite) seedDurableRows(prefix string, n int) []string {
	r := s.issueRepo()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%03d", prefix, i)
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, fmt.Sprintf("row %d", i)), "tester", domain.InsertIssueOpts{}))
		ids = append(ids, id)
	}
	return ids
}

// perTableGoSideSortKeepsTheRightSubset is bd-69c1a's plain case on the
// id-shrink path (Limit>0 routes searchTable through scanFilterIDs). The id
// query renders no ORDER BY for a Go-side sort and searchWindowFor pushes no
// page bound, so the whole matching set arrives — but before the fix nothing
// sorted it before finishWindow's trim, and the page was an arbitrary
// engine-ordered subset. The ids are zero-padded, so byte order and natural
// order agree and the answer is unambiguous.
func (s *testSuite) perTableGoSideSortKeepsTheRightSubset() {
	const prefix = "bd-ptw-gos"
	ids := s.seedDurableRows(prefix, 10)
	r := s.issueRepo()

	const limit = 3
	page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: prefix + "-", SortBy: "id", Limit: limit})
	s.Require().NoError(err)

	// NOTE: a weak witness by itself — Dolt's engine order for a PK-clustered
	// id-prefix scan tends to be byte-ascending already, so this case likely
	// passed pre-fix too. It pins the contract against future engine-order
	// changes; the DESC cases below are the red-before-green fences.
	s.Equal(ids[:limit], idsFrom(page),
		"a limited per-table page under a Go-side sort must be the globally first rows, not an engine-ordered subset")
	s.True(page.HasMore)
}

// perTableGoSideSortDescKeepsTheByteLastRows is the measured bd-69c1a repro:
// 10 rows, sort id desc, limit 3 answered [000 001 002] where [009 008 007]
// was owed — membership AND order wrong, because nothing sorted before the
// trim and sqlbuild.Less ignored sortDesc for "id".
func (s *testSuite) perTableGoSideSortDescKeepsTheByteLastRows() {
	const prefix = "bd-ptw-desc"
	ids := s.seedDurableRows(prefix, 10)
	r := s.issueRepo()

	page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: prefix + "-", SortBy: "id", SortDesc: true, Limit: 3})
	s.Require().NoError(err)

	s.Equal([]string{ids[9], ids[8], ids[7]}, idsFrom(page),
		"a reversed id page must be the byte-last rows first")
	s.True(page.HasMore)
}

// perTableRoutesAnswerOnePage is the cross-route parity half of the bd-jao3t/
// bd-69c1a fix: over one durable-only corpus, the wisps-empty probe route, the
// SkipWisps route, the wide (NoIDShrink) path, and the counts twin must all
// answer the identical reversed page. Before the fix each trimmed an unsorted
// set, so agreement was engine-order luck.
func (s *testSuite) perTableRoutesAnswerOnePage() {
	const prefix = "bd-ptw-par"
	ids := s.seedDurableRows(prefix, 8)
	r := s.issueRepo()

	want := []string{ids[7], ids[6], ids[5]}
	for _, tc := range []struct {
		route  string
		filter types.IssueFilter
	}{
		{"the wisps-empty probe route", types.IssueFilter{IDPrefix: prefix + "-", SortBy: "id", SortDesc: true, Limit: 3}},
		{"the SkipWisps route", types.IssueFilter{IDPrefix: prefix + "-", SortBy: "id", SortDesc: true, Limit: 3, SkipWisps: true}},
		{"the wide path", types.IssueFilter{IDPrefix: prefix + "-", SortBy: "id", SortDesc: true, Limit: 3, NoIDShrink: true}},
	} {
		page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "", tc.filter)
		s.Require().NoError(err, tc.route)
		s.Equal(want, idsFrom(page), "%s must answer the one true page", tc.route)
		s.True(page.HasMore, tc.route)
	}

	counts, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: prefix + "-", SortBy: "id", SortDesc: true, Limit: 3})
	s.Require().NoError(err)
	got := make([]string, 0, len(counts.Items))
	for _, item := range counts.Items {
		got = append(got, item.Issue.ID)
	}
	s.Equal(want, got, "the counts twin must answer the same page as the plain twin")
	s.True(counts.HasMore)
}

// unionGoSideSortDescKeepsTheByteLastRows pins the reversed page on the union
// seam. idSrcPage.sortGoSide honored sortDesc from the start, so this is a
// regression fence rather than a fix witness — it is here because the per-table
// cases above assert the same page shape, and the three seams answering one
// page is the contract the pair of beads restores. Seeds wisps; keep it LAST.
func (s *testSuite) unionGoSideSortDescKeepsTheByteLastRows() {
	const prefix = "bd-ptw-udesc"
	ids := s.seedTwoPlanes(prefix, 6)
	r := s.issueRepo()

	page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: prefix + "-", SortBy: "id", SortDesc: true, Limit: 5})
	s.Require().NoError(err)

	want := []string{ids[11], ids[10], ids[9], ids[8], ids[7]}
	s.Equal(want, idsFrom(page),
		"a reversed union page must be the byte-last rows across both planes")
	s.True(page.HasMore)
}
