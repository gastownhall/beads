package db

import (
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueSearchAcrossIssuesAndWisps() {
	s.Run("MergesIssuesAndWisps", s.searchAcrossMergesTables)
	s.Run("SkipWispsReturnsOnlyIssues", s.searchAcrossSkipWisps)
	s.Run("EphemeralTrueReturnsOnlyWisps", s.searchAcrossEphemeralTrueOnlyWisps)
	s.Run("FilterByStatusAppliesAcrossTables", s.searchAcrossStatusFilter)
	s.Run("FilterByLabelAppliesAcrossTables", s.searchAcrossLabelFilter)
	s.Run("LabelHydrationOnResults", s.searchAcrossLabelHydration)
	s.Run("LimitRespectedPerTable", s.searchAcrossLimitRespected)
	s.Run("CollisionAcrossTablesIsError", s.searchAcrossCollisionError)
	s.Run("SkipLabelsLeavesLabelsNil", s.searchAcrossSkipLabels)
}

func (s *testSuite) searchAcrossMergesTables() {
	r := s.issueRepo()
	perm := newTestIssue("bd-srx-merge-perm", "perm one")
	s.Require().NoError(r.Insert(s.Ctx(), perm, "tester", domain.InsertIssueOpts{}))

	w := newTestIssue("bd-srx-merge-wisp", "wisp one")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	out, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "", types.IssueFilter{IDPrefix: "bd-srx-merge-"})
	s.Require().NoError(err)
	gotIDs := idsFrom(out)
	s.Contains(gotIDs, "bd-srx-merge-perm")
	s.Contains(gotIDs, "bd-srx-merge-wisp")
}

func (s *testSuite) searchAcrossSkipWisps() {
	r := s.issueRepo()
	perm := newTestIssue("bd-srx-skip-perm", "perm")
	s.Require().NoError(r.Insert(s.Ctx(), perm, "tester", domain.InsertIssueOpts{}))

	w := newTestIssue("bd-srx-skip-wisp", "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	out, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srx-skip-", SkipWisps: true})
	s.Require().NoError(err)
	gotIDs := idsFrom(out)
	s.Contains(gotIDs, "bd-srx-skip-perm")
	s.NotContains(gotIDs, "bd-srx-skip-wisp", "SkipWisps must exclude wisps from results")
}

func (s *testSuite) searchAcrossEphemeralTrueOnlyWisps() {
	r := s.issueRepo()
	perm := newTestIssue("bd-srx-eph-perm", "perm")
	s.Require().NoError(r.Insert(s.Ctx(), perm, "tester", domain.InsertIssueOpts{}))

	w := newTestIssue("bd-srx-eph-wisp", "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	ephTrue := true
	out, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srx-eph-", Ephemeral: &ephTrue})
	s.Require().NoError(err)
	gotIDs := idsFrom(out)
	s.Contains(gotIDs, "bd-srx-eph-wisp")
	s.NotContains(gotIDs, "bd-srx-eph-perm", "Ephemeral=true must route to wisps table only")
}

func (s *testSuite) searchAcrossStatusFilter() {
	r := s.issueRepo()
	openIssue := newTestIssue("bd-srx-st-open", "open")
	s.Require().NoError(r.Insert(s.Ctx(), openIssue, "tester", domain.InsertIssueOpts{}))

	closedIssue := newTestIssue("bd-srx-st-closed", "closed")
	closedIssue.Status = types.StatusClosed
	s.Require().NoError(r.Insert(s.Ctx(), closedIssue, "tester", domain.InsertIssueOpts{}))

	closedWisp := newTestIssue("bd-srx-st-wclosed", "wisp closed")
	closedWisp.Ephemeral = true
	closedWisp.Status = types.StatusClosed
	s.Require().NoError(r.Insert(s.Ctx(), closedWisp, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	closed := types.StatusClosed
	out, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srx-st-", Status: &closed})
	s.Require().NoError(err)
	gotIDs := idsFrom(out)
	s.ElementsMatch([]string{"bd-srx-st-closed", "bd-srx-st-wclosed"}, gotIDs)
}

func (s *testSuite) searchAcrossLabelFilter() {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())

	hot := newTestIssue("bd-srx-lbl-hot", "tagged hot")
	s.Require().NoError(r.Insert(s.Ctx(), hot, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srx-lbl-hot", "hot", "tester", domain.LabelOpts{}))

	cold := newTestIssue("bd-srx-lbl-cold", "tagged cold")
	s.Require().NoError(r.Insert(s.Ctx(), cold, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srx-lbl-cold", "cold", "tester", domain.LabelOpts{}))

	wispHot := newTestIssue("bd-srx-lbl-whot", "wisp hot")
	wispHot.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), wispHot, "tester", domain.InsertIssueOpts{UseWispsTable: true}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srx-lbl-whot", "hot", "tester", domain.LabelOpts{UseWispsTable: true}))

	out, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srx-lbl-", Labels: []string{"hot"}})
	s.Require().NoError(err)
	gotIDs := idsFrom(out)
	s.ElementsMatch([]string{"bd-srx-lbl-hot", "bd-srx-lbl-whot"}, gotIDs)
}

func (s *testSuite) searchAcrossLabelHydration() {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())

	issue := newTestIssue("bd-srx-hyd-1", "labeled")
	s.Require().NoError(r.Insert(s.Ctx(), issue, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srx-hyd-1", "alpha", "tester", domain.LabelOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srx-hyd-1", "beta", "tester", domain.LabelOpts{}))

	out, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srx-hyd-"})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1)
	s.ElementsMatch([]string{"alpha", "beta"}, out.Items[0].Labels)
}

func (s *testSuite) searchAcrossLimitRespected() {
	r := s.issueRepo()
	for i := 0; i < 5; i++ {
		s.Require().NoError(r.Insert(s.Ctx(),
			newTestIssue("bd-srx-lim-"+string(rune('a'+i)), "x"),
			"tester", domain.InsertIssueOpts{}))
	}
	out, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srx-lim-", Limit: 3, SkipWisps: true})
	s.Require().NoError(err)
	s.Len(out.Items, 3)
}

func (s *testSuite) searchAcrossCollisionError() {
	r := s.issueRepo()
	const id = "bd-srx-collision-1"
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, "perm"), "tester", domain.InsertIssueOpts{}))

	w := newTestIssue(id, "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	_, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srx-collision-"})
	s.Require().Error(err)
	s.Contains(err.Error(), "exists in both issues and wisps")
}

func (s *testSuite) searchAcrossSkipLabels() {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())

	issue := newTestIssue("bd-srx-nolbl-1", "labeled")
	s.Require().NoError(r.Insert(s.Ctx(), issue, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srx-nolbl-1", "gamma", "tester", domain.LabelOpts{}))

	out, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srx-nolbl-", SkipLabels: true, SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1)
	s.Empty(out.Items[0].Labels, "SkipLabels must leave Labels nil/empty")
}

func idsFrom(page domain.SearchPage) []string {
	out := make([]string, 0, len(page.Items))
	for _, issue := range page.Items {
		out = append(out, issue.ID)
	}
	return out
}

func (s *testSuite) TestPagination() {
	s.Run("SearchIssues", func() {
		s.Run("SingleTable_LimitOnly", s.paginationSearchSingleLimit)
		s.Run("SingleTable_OffsetOnly", s.paginationSearchSingleOffset)
		s.Run("SingleTable_LimitAndOffset", s.paginationSearchSingleLimitOffset)
		s.Run("SingleTable_HasMoreTransitions", s.paginationSearchSingleHasMore)
		s.Run("SingleTable_PageWalkCoversAll", s.paginationSearchSinglePageWalk)
		s.Run("UnionAcross_LimitOnly", s.paginationSearchUnionLimit)
		s.Run("UnionAcross_OffsetOnly", s.paginationSearchUnionOffset)
		s.Run("UnionAcross_LimitAndOffset", s.paginationSearchUnionLimitOffset)
		s.Run("UnionAcross_PageWalkCoversAll", s.paginationSearchUnionPageWalk)
		s.Run("UnionAcross_InterleavedByCreatedAt", s.paginationSearchUnionInterleaved)
		s.Run("SortDesc_ReversesOrder", s.paginationSearchSortDesc)
		s.Run("SortByCreated_PaginatesStably", s.paginationSearchSortByCreated)
	})
	s.Run("SearchIssuesWithCounts", func() {
		s.Run("SingleTable_LimitAndOffset", s.paginationCountsSingleLimitOffset)
		s.Run("UnionAcross_LimitAndOffset", s.paginationCountsUnionLimitOffset)
		s.Run("UnionAcross_PageWalkCoversAll", s.paginationCountsUnionPageWalk)
	})
	s.Run("GetReadyWork", func() {
		s.Run("LimitAndOffset", s.paginationReadyLimitOffset)
		s.Run("HasMoreAtBoundary", s.paginationReadyHasMoreBoundary)
		s.Run("PageWalkCoversAll", s.paginationReadyPageWalk)
		s.Run("UnionInterleavesIssuesAndWisps", s.paginationReadyUnionInterleaves)
	})
	s.Run("GetReadyWorkWithCounts", func() {
		s.Run("LimitAndOffset", s.paginationReadyCountsLimitOffset)
		s.Run("PageWalkCoversAll", s.paginationReadyCountsPageWalk)
	})
}

func (s *testSuite) paginationTestSeed(prefix string, n int) []string {
	r := s.issueRepo()
	type rec struct {
		id        string
		priority  int
		createdAt time.Time
	}
	now := time.Now().UTC().Add(-time.Duration(n) * time.Minute)
	recs := make([]rec, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%02d", prefix, i)
		iss := newTestIssue(id, fmt.Sprintf("item %d", i))
		iss.Priority = i % 3
		iss.CreatedAt = now.Add(time.Duration(i) * time.Minute)
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
		recs = append(recs, rec{id: id, priority: iss.Priority, createdAt: iss.CreatedAt})
	}

	expected := make([]rec, len(recs))
	copy(expected, recs)
	for i := 0; i < len(expected); i++ {
		for j := i + 1; j < len(expected); j++ {
			a, b := expected[i], expected[j]
			less := false
			switch {
			case a.priority != b.priority:
				less = a.priority < b.priority
			case !a.createdAt.Equal(b.createdAt):
				less = a.createdAt.After(b.createdAt)
			default:
				less = a.id < b.id
			}
			if !less {
				expected[i], expected[j] = expected[j], expected[i]
			}
		}
	}
	out := make([]string, len(expected))
	for i, e := range expected {
		out[i] = e.id
	}
	return out
}

func (s *testSuite) paginationSearchSingleLimit() {
	expected := s.paginationTestSeed("bd-pgs-l", 10)
	r := s.issueRepo()

	page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-pgs-l-", Limit: 3, SkipWisps: true})
	s.Require().NoError(err)
	s.Equal(expected[:3], idsFrom(page))
	s.True(page.HasMore, "HasMore must be true when more matches exist")
}

func (s *testSuite) paginationSearchSingleOffset() {
	expected := s.paginationTestSeed("bd-pgs-o", 10)
	r := s.issueRepo()

	page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-pgs-o-", Limit: 100, Offset: 4, SkipWisps: true})
	s.Require().NoError(err)
	s.Equal(expected[4:], idsFrom(page))
	s.False(page.HasMore, "HasMore must be false when page covers tail")
}

func (s *testSuite) paginationSearchSingleLimitOffset() {
	expected := s.paginationTestSeed("bd-pgs-lo", 10)
	r := s.issueRepo()

	page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-pgs-lo-", Limit: 3, Offset: 3, SkipWisps: true})
	s.Require().NoError(err)
	s.Equal(expected[3:6], idsFrom(page))
	s.True(page.HasMore, "HasMore must be true when more matches follow")
}

func (s *testSuite) paginationSearchSingleHasMore() {
	expected := s.paginationTestSeed("bd-pgs-hm", 5)
	r := s.issueRepo()

	page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-pgs-hm-", Limit: 3, Offset: 2, SkipWisps: true})
	s.Require().NoError(err)
	s.Equal(expected[2:], idsFrom(page))
	s.False(page.HasMore, "HasMore=false when Offset+Limit covers exactly the remainder")

	page2, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-pgs-hm-", Limit: 2, Offset: 2, SkipWisps: true})
	s.Require().NoError(err)
	s.Equal(expected[2:4], idsFrom(page2))
	s.True(page2.HasMore, "HasMore=true when more rows follow the page")
}

func (s *testSuite) paginationSearchSinglePageWalk() {
	expected := s.paginationTestSeed("bd-pgs-pw", 11)
	r := s.issueRepo()

	const pageSize = 4
	var walked []string
	offset := 0
	for {
		page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
			types.IssueFilter{IDPrefix: "bd-pgs-pw-", Limit: pageSize, Offset: offset, SkipWisps: true})
		s.Require().NoError(err)
		walked = append(walked, idsFrom(page)...)
		if !page.HasMore {
			break
		}
		offset += pageSize
	}
	s.Equal(expected, walked, "page walk must reconstruct full sorted result with no gaps or dupes")
}

func (s *testSuite) paginationSeedAcross(permPrefix, wispPrefix string, n, m int) []string {
	r := s.issueRepo()
	now := time.Now().UTC().Add(-time.Duration(n+m) * time.Minute)

	type rec struct {
		id        string
		priority  int
		createdAt time.Time
	}
	var recs []rec

	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%02d", permPrefix, i)
		iss := newTestIssue(id, fmt.Sprintf("perm %d", i))
		iss.Priority = i % 3
		iss.CreatedAt = now.Add(time.Duration(2*i) * time.Minute)
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
		recs = append(recs, rec{id: id, priority: iss.Priority, createdAt: iss.CreatedAt})
	}
	for i := 0; i < m; i++ {
		id := fmt.Sprintf("%s-%02d", wispPrefix, i)
		w := newTestIssue(id, fmt.Sprintf("wisp %d", i))
		w.Priority = (i + 1) % 3
		w.CreatedAt = now.Add(time.Duration(2*i+1) * time.Minute)
		w.Ephemeral = true
		s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))
		recs = append(recs, rec{id: id, priority: w.Priority, createdAt: w.CreatedAt})
	}

	expected := make([]rec, len(recs))
	copy(expected, recs)
	for i := 0; i < len(expected); i++ {
		for j := i + 1; j < len(expected); j++ {
			a, b := expected[i], expected[j]
			less := false
			switch {
			case a.priority != b.priority:
				less = a.priority < b.priority
			case !a.createdAt.Equal(b.createdAt):
				less = a.createdAt.After(b.createdAt)
			default:
				less = a.id < b.id
			}
			if !less {
				expected[i], expected[j] = expected[j], expected[i]
			}
		}
	}
	out := make([]string, len(expected))
	for i, e := range expected {
		out[i] = e.id
	}
	return out
}

func (s *testSuite) paginationSearchUnionLimit() {
	expected := s.paginationSeedAcross("bd-pgu-l", "bd-pgu-lw", 5, 5)
	r := s.issueRepo()

	page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-pgu-l", Limit: 4})
	s.Require().NoError(err)
	s.Equal(expected[:4], idsFrom(page))
	s.True(page.HasMore)
}

func (s *testSuite) paginationSearchUnionOffset() {
	expected := s.paginationSeedAcross("bd-pgu-o", "bd-pgu-ow", 5, 5)
	r := s.issueRepo()

	page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-pgu-o", Limit: 100, Offset: 6})
	s.Require().NoError(err)
	s.Equal(expected[6:], idsFrom(page))
	s.False(page.HasMore)
}

func (s *testSuite) paginationSearchUnionLimitOffset() {
	expected := s.paginationSeedAcross("bd-pgu-lo", "bd-pgu-low", 5, 5)
	r := s.issueRepo()

	page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-pgu-lo", Limit: 3, Offset: 3})
	s.Require().NoError(err)
	s.Equal(expected[3:6], idsFrom(page))
	s.True(page.HasMore)
}

func (s *testSuite) paginationSearchUnionPageWalk() {
	expected := s.paginationSeedAcross("bd-pgu-pw", "bd-pgu-pww", 6, 5)
	r := s.issueRepo()

	const pageSize = 3
	var walked []string
	offset := 0
	for {
		page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
			types.IssueFilter{IDPrefix: "bd-pgu-pw", Limit: pageSize, Offset: offset})
		s.Require().NoError(err)
		walked = append(walked, idsFrom(page)...)
		if !page.HasMore {
			break
		}
		offset += pageSize
	}
	s.Equal(expected, walked, "UNION ALL page walk must reconstruct global order with no gaps or dupes")
}

func (s *testSuite) paginationSearchUnionInterleaved() {
	r := s.issueRepo()
	now := time.Now().UTC()

	wantOrder := []string{
		"bd-pgu-int-w-2", // newest wisp
		"bd-pgu-int-i-2",
		"bd-pgu-int-w-1",
		"bd-pgu-int-i-1",
		"bd-pgu-int-w-0",
		"bd-pgu-int-i-0", // oldest
	}

	for i := 0; i < 3; i++ {
		iss := newTestIssue(fmt.Sprintf("bd-pgu-int-i-%d", i), "perm")
		iss.Priority = 1
		iss.CreatedAt = now.Add(time.Duration(2*i) * time.Minute)
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))

		w := newTestIssue(fmt.Sprintf("bd-pgu-int-w-%d", i), "wisp")
		w.Priority = 1
		w.CreatedAt = now.Add(time.Duration(2*i+1) * time.Minute)
		w.Ephemeral = true
		s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))
	}

	page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-pgu-int-"})
	s.Require().NoError(err)
	s.Equal(wantOrder, idsFrom(page), "UNION must interleave issues/wisps by global ORDER BY")
}

func (s *testSuite) paginationSearchSortDesc() {
	expected := s.paginationTestSeed("bd-pgs-sd", 6)
	r := s.issueRepo()

	reversedExpected := computeExpected(s, "bd-pgs-sd", 6, true)
	_ = expected

	page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-pgs-sd-", SkipWisps: true, SortDesc: true})
	s.Require().NoError(err)
	s.Equal(reversedExpected, idsFrom(page))
}

func computeExpected(s *testSuite, prefix string, n int, primaryDesc bool) []string {
	type rec struct {
		id        string
		priority  int
		createdAt time.Time
	}
	now := time.Now().UTC().Add(-time.Duration(n) * time.Minute)
	recs := make([]rec, 0, n)
	for i := 0; i < n; i++ {
		recs = append(recs, rec{
			id:        fmt.Sprintf("%s-%02d", prefix, i),
			priority:  i % 3,
			createdAt: now.Add(time.Duration(i) * time.Minute),
		})
	}
	for i := 0; i < len(recs); i++ {
		for j := i + 1; j < len(recs); j++ {
			a, b := recs[i], recs[j]
			less := false
			switch {
			case a.priority != b.priority:
				if primaryDesc {
					less = a.priority > b.priority
				} else {
					less = a.priority < b.priority
				}
			case !a.createdAt.Equal(b.createdAt):
				less = a.createdAt.After(b.createdAt)
			default:
				less = a.id < b.id
			}
			if !less {
				recs[i], recs[j] = recs[j], recs[i]
			}
		}
	}
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.id
	}
	return out
}

func (s *testSuite) paginationSearchSortByCreated() {
	r := s.issueRepo()
	now := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		iss := newTestIssue(fmt.Sprintf("bd-pgs-sc-%02d", i), "x")
		iss.CreatedAt = now.Add(time.Duration(i) * time.Minute)
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
	}

	expected := []string{
		"bd-pgs-sc-05", "bd-pgs-sc-04", "bd-pgs-sc-03",
		"bd-pgs-sc-02", "bd-pgs-sc-01", "bd-pgs-sc-00",
	}

	var walked []string
	for off := 0; ; off += 2 {
		page, err := r.SearchAcrossIssuesAndWisps(s.Ctx(), "",
			types.IssueFilter{IDPrefix: "bd-pgs-sc-", Limit: 2, Offset: off, SkipWisps: true, SortBy: "created"})
		s.Require().NoError(err)
		walked = append(walked, idsFrom(page)...)
		if !page.HasMore {
			break
		}
	}
	s.Equal(expected, walked)
}

func (s *testSuite) paginationCountsSingleLimitOffset() {
	expected := s.paginationTestSeed("bd-pgc-s", 8)
	r := s.issueRepo()

	page, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-pgc-s-", Limit: 3, Offset: 2, SkipWisps: true})
	s.Require().NoError(err)
	s.Equal(expected[2:5], iwcIDs(page))
	s.True(page.HasMore)
}

func (s *testSuite) paginationCountsUnionLimitOffset() {
	expected := s.paginationSeedAcross("bd-pgc-u", "bd-pgc-uw", 4, 4)
	r := s.issueRepo()

	page, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-pgc-u", Limit: 3, Offset: 2})
	s.Require().NoError(err)
	s.Equal(expected[2:5], iwcIDs(page))
	s.True(page.HasMore)
}

func (s *testSuite) paginationCountsUnionPageWalk() {
	expected := s.paginationSeedAcross("bd-pgc-pw", "bd-pgc-pww", 5, 4)
	r := s.issueRepo()

	const pageSize = 3
	var walked []string
	offset := 0
	for {
		page, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
			types.IssueFilter{IDPrefix: "bd-pgc-pw", Limit: pageSize, Offset: offset})
		s.Require().NoError(err)
		walked = append(walked, iwcIDs(page)...)
		if !page.HasMore {
			break
		}
		offset += pageSize
	}
	s.Equal(expected, walked, "WithCounts UNION page walk must reconstruct full order")
}

func (s *testSuite) paginationSeedReady(prefix, isolationLabel string, n int) []string {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())
	now := time.Now().UTC().Add(-time.Duration(n) * time.Minute)
	expected := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%02d", prefix, i)
		iss := newTestIssue(id, "ready")
		iss.Priority = 1
		iss.CreatedAt = now.Add(time.Duration(i) * time.Minute)
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
		s.Require().NoError(labelRepo.Insert(s.Ctx(), id, isolationLabel, "tester", domain.LabelOpts{}))
		expected = append(expected, id) // append → oldest-first (FIFO)
	}
	return expected
}

func (s *testSuite) paginationReadyLimitOffset() {
	const label = "pg-ready-lo"
	expected := s.paginationSeedReady("bd-pgr-lo", label, 10)
	r := s.issueRepo()

	page, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{
		Labels:     []string{label},
		Limit:      4,
		Offset:     2,
		SortPolicy: types.SortPolicyPriority,
	})
	s.Require().NoError(err)
	s.Equal(expected[2:6], idsFrom(page))
	s.True(page.HasMore)
}

func (s *testSuite) paginationReadyHasMoreBoundary() {
	const label = "pg-ready-hm"
	expected := s.paginationSeedReady("bd-pgr-hm", label, 6)
	r := s.issueRepo()

	page, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{
		Labels:     []string{label},
		Limit:      3,
		Offset:     3,
		SortPolicy: types.SortPolicyPriority,
	})
	s.Require().NoError(err)
	s.Equal(expected[3:], idsFrom(page))
	s.False(page.HasMore)

	page2, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{
		Labels:     []string{label},
		Limit:      2,
		Offset:     3,
		SortPolicy: types.SortPolicyPriority,
	})
	s.Require().NoError(err)
	s.Equal(expected[3:5], idsFrom(page2))
	s.True(page2.HasMore)
}

func (s *testSuite) paginationReadyPageWalk() {
	const label = "pg-ready-pw"
	expected := s.paginationSeedReady("bd-pgr-pw", label, 9)
	r := s.issueRepo()

	walked := s.readyPageWalkByLabel(r, label, types.SortPolicyPriority, 3)
	s.Equal(expected, walked, "ready page walk must reconstruct full order with no gaps or dupes")
}

func (s *testSuite) paginationReadyUnionInterleaves() {
	const label = "pg-ready-int"
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())
	now := time.Now().UTC()

	for i := 0; i < 3; i++ {
		iss := newTestIssue(fmt.Sprintf("bd-pgr-int-i-%d", i), "perm")
		iss.Priority = 1
		iss.CreatedAt = now.Add(time.Duration(2*i) * time.Second)
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
		s.Require().NoError(labelRepo.Insert(s.Ctx(), iss.ID, label, "tester", domain.LabelOpts{}))

		// NoHistory wisp (ephemeral=0): default ready work excludes true
		// ephemerals on both stacks, and the interleaving under test is
		// about the union page walk, not the ephemeral predicate.
		w := newTestIssue(fmt.Sprintf("bd-pgr-int-w-%d", i), "wisp")
		w.Priority = 1
		w.CreatedAt = now.Add(time.Duration(2*i+1) * time.Second)
		s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))
		s.Require().NoError(labelRepo.Insert(s.Ctx(), w.ID, label, "tester", domain.LabelOpts{UseWispsTable: true}))
	}

	want := []string{
		"bd-pgr-int-i-0", "bd-pgr-int-w-0",
		"bd-pgr-int-i-1", "bd-pgr-int-w-1",
		"bd-pgr-int-i-2", "bd-pgr-int-w-2",
	}

	walked := s.readyPageWalkByLabel(r, label, types.SortPolicyPriority, 2)
	s.Equal(want, walked, "UNION ALL must interleave wisps with issues by created_at across page boundaries")
}

func (s *testSuite) paginationReadyCountsLimitOffset() {
	const label = "pg-readyc-lo"
	expected := s.paginationSeedReady("bd-pgrc-lo", label, 8)
	r := s.issueRepo()

	page, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{
		Labels:     []string{label},
		Limit:      3,
		Offset:     2,
		SortPolicy: types.SortPolicyPriority,
	})
	s.Require().NoError(err)
	s.Equal(expected[2:5], iwcIDs(page))
	s.True(page.HasMore)
}

func (s *testSuite) paginationReadyCountsPageWalk() {
	const label = "pg-readyc-pw"
	expected := s.paginationSeedReady("bd-pgrc-pw", label, 7)
	r := s.issueRepo()

	walked := s.readyCountsPageWalkByLabel(r, label, types.SortPolicyPriority, 2)
	s.Equal(expected, walked, "ready counts page walk must reconstruct full order")
}

func (s *testSuite) readyPageWalkByLabel(r domain.IssueSQLRepository, label string, policy types.SortPolicy, pageSize int) []string {
	var walked []string
	seen := make(map[string]bool)
	offset := 0
	for {
		page, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{
			Labels:     []string{label},
			Limit:      pageSize,
			Offset:     offset,
			SortPolicy: policy,
		})
		s.Require().NoError(err)
		for _, id := range idsFrom(page) {
			if seen[id] {
				continue
			}
			seen[id] = true
			walked = append(walked, id)
		}
		if !page.HasMore {
			break
		}
		offset += pageSize
	}
	return walked
}

func (s *testSuite) readyCountsPageWalkByLabel(r domain.IssueSQLRepository, label string, policy types.SortPolicy, pageSize int) []string {
	var walked []string
	seen := make(map[string]bool)
	offset := 0
	for {
		page, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{
			Labels:     []string{label},
			Limit:      pageSize,
			Offset:     offset,
			SortPolicy: policy,
		})
		s.Require().NoError(err)
		for _, id := range iwcIDs(page) {
			if seen[id] {
				continue
			}
			seen[id] = true
			walked = append(walked, id)
		}
		if !page.HasMore {
			break
		}
		offset += pageSize
	}
	return walked
}
