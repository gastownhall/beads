package db

import (
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueUseCase_ClaimReadyIssue() {
	s.Run("WrapsClaimedTrueOnSuccess", s.ucClaimReadyWrapsClaimed)
	s.Run("NoReadyReturnsClaimedFalse", s.ucClaimReadyEmpty)
	s.Run("AppliesAssigneeAndStatus", s.ucClaimReadyAppliesState)
	s.Run("PropagatesFilter", s.ucClaimReadyFilter)
}

func (s *testSuite) TestIssueUseCase_ClaimReadyWisp() {
	s.Run("DelegatesToSharedReadyClaim", s.ucClaimReadyWispDelegates)
	s.Run("NoReadyReturnsClaimedFalse", s.ucClaimReadyWispEmpty)
}

func (s *testSuite) TestIssueUseCase_GetBlockedIssues() {
	s.Run("PassesThroughBlockedListing", s.ucBlockedPassesThrough)
	s.Run("EmptyDBReturnsEmpty", s.ucBlockedEmpty)
	s.Run("ParentIDFilterHonored", s.ucBlockedParentFilter)
}

func (s *testSuite) TestIssueUseCase_GetStatistics() {
	s.Run("EmptyDBReturnsZeroes", s.ucStatsEmpty)
	s.Run("AggregatesAcrossStatuses", s.ucStatsAggregates)
	s.Run("ReadyDerived", s.ucStatsReadyDerived)
}

func (s *testSuite) TestDependencyUseCase_DetectCycles() {
	s.Run("NoEdgesReturnsEmpty", s.ucCyclesEmpty)
	s.Run("CycleDetected", s.ucCyclesDetected)
	s.Run("AcyclicReturnsEmpty", s.ucCyclesAcyclic)
}

func (s *testSuite) TestIssueUseCase_GetReadyWork_Pagination() {
	s.Run("OffsetAndLimitRoundTripThroughUC", s.ucReadyWorkPaginationRoundTrip)
}

// ---------- ClaimReadyIssue UC ----------

func (s *testSuite) ucClaimReadyWrapsClaimed() {
	s.resetDB()
	uc := s.issueUseCase()
	r := s.issueRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue("bd-uccr-a", "alpha"), "tester", domain.InsertIssueOpts{}))

	res, err := uc.ClaimReadyIssue(s.Ctx(), types.WorkFilter{}, "alice")
	s.Require().NoError(err)
	s.True(res.Claimed, "Claimed must be true when an issue was claimed")
	s.Require().NotNil(res.Issue)
	s.Equal("bd-uccr-a", res.Issue.ID)
}

func (s *testSuite) ucClaimReadyEmpty() {
	s.resetDB()
	uc := s.issueUseCase()

	res, err := uc.ClaimReadyIssue(s.Ctx(), types.WorkFilter{}, "alice")
	s.Require().NoError(err)
	s.False(res.Claimed, "Claimed must be false when nothing was claimable")
	s.Nil(res.Issue, "Issue must be nil when nothing was claimable")
}

func (s *testSuite) ucClaimReadyAppliesState() {
	s.resetDB()
	uc := s.issueUseCase()
	r := s.issueRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue("bd-uccr-st", "x"), "tester", domain.InsertIssueOpts{}))

	res, err := uc.ClaimReadyIssue(s.Ctx(), types.WorkFilter{}, "alice")
	s.Require().NoError(err)
	s.Require().True(res.Claimed)

	got, err := r.Get(s.Ctx(), "bd-uccr-st", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal(types.StatusInProgress, got.Status)
	s.Equal("alice", got.Assignee)
}

func (s *testSuite) ucClaimReadyFilter() {
	s.resetDB()
	uc := s.issueUseCase()
	r := s.issueRepo()
	low := newTestIssue("bd-uccr-lo", "lo")
	low.Priority = 3
	s.Require().NoError(r.Insert(s.Ctx(), low, "tester", domain.InsertIssueOpts{}))
	high := newTestIssue("bd-uccr-hi", "hi")
	high.Priority = 1
	s.Require().NoError(r.Insert(s.Ctx(), high, "tester", domain.InsertIssueOpts{}))

	p := 1
	res, err := uc.ClaimReadyIssue(s.Ctx(), types.WorkFilter{Priority: &p}, "alice")
	s.Require().NoError(err)
	s.Require().True(res.Claimed)
	s.Equal("bd-uccr-hi", res.Issue.ID, "UC must pass the filter through to the repo")
}

// ---------- ClaimReadyWisp UC ----------

func (s *testSuite) ucClaimReadyWispDelegates() {
	s.resetDB()
	uc := s.issueUseCase()
	r := s.issueRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue("bd-uccrw-a", "alpha"), "tester", domain.InsertIssueOpts{}))

	res, err := uc.ClaimReadyWisp(s.Ctx(), types.WorkFilter{}, "alice")
	s.Require().NoError(err)
	s.True(res.Claimed)
	s.Require().NotNil(res.Issue)
	s.Equal("bd-uccrw-a", res.Issue.ID)
}

func (s *testSuite) ucClaimReadyWispEmpty() {
	s.resetDB()
	uc := s.issueUseCase()

	res, err := uc.ClaimReadyWisp(s.Ctx(), types.WorkFilter{}, "alice")
	s.Require().NoError(err)
	s.False(res.Claimed)
	s.Nil(res.Issue)
}

// ---------- GetBlockedIssues UC ----------

func (s *testSuite) ucBlockedPassesThrough() {
	s.resetDB()
	uc := s.issueUseCase()
	r := s.issueRepo()
	dr := s.depRepo()
	src := newTestIssue("bd-ucbl-src", "blocked")
	s.Require().NoError(r.Insert(s.Ctx(), src, "tester", domain.InsertIssueOpts{}))
	tgt := newTestIssue("bd-ucbl-tgt", "blocker")
	s.Require().NoError(r.Insert(s.Ctx(), tgt, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-ucbl-src", "bd-ucbl-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := uc.GetBlockedIssues(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.Require().Len(out, 1)
	s.Equal("bd-ucbl-src", out[0].ID)
	s.Equal([]string{"bd-ucbl-tgt"}, out[0].BlockedBy)
}

func (s *testSuite) ucBlockedEmpty() {
	s.resetDB()
	uc := s.issueUseCase()
	out, err := uc.GetBlockedIssues(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) ucBlockedParentFilter() {
	s.resetDB()
	uc := s.issueUseCase()
	r := s.issueRepo()
	dr := s.depRepo()
	parent := newTestIssue("bd-ucblp-parent", "parent")
	s.Require().NoError(r.Insert(s.Ctx(), parent, "tester", domain.InsertIssueOpts{}))
	inside := newTestIssue("bd-ucblp-parent.1", "inside")
	s.Require().NoError(r.Insert(s.Ctx(), inside, "tester", domain.InsertIssueOpts{}))
	outside := newTestIssue("bd-ucblp-out", "outside")
	s.Require().NoError(r.Insert(s.Ctx(), outside, "tester", domain.InsertIssueOpts{}))
	blocker := newTestIssue("bd-ucblp-blocker", "blocker")
	s.Require().NoError(r.Insert(s.Ctx(), blocker, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-ucblp-parent.1", "bd-ucblp-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-ucblp-parent.1", "bd-ucblp-blocker", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-ucblp-out", "bd-ucblp-blocker", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	pid := "bd-ucblp-parent"
	out, err := uc.GetBlockedIssues(s.Ctx(), types.WorkFilter{ParentID: &pid})
	s.Require().NoError(err)
	ids := blockedIDs(out)
	s.Contains(ids, "bd-ucblp-parent.1")
	s.NotContains(ids, "bd-ucblp-out", "UC must forward ParentID filter")
}

// ---------- GetStatistics UC ----------

func (s *testSuite) ucStatsEmpty() {
	s.resetDB()
	uc := s.issueUseCase()
	stats, err := uc.GetStatistics(s.Ctx())
	s.Require().NoError(err)
	s.Equal(0, stats.TotalIssues)
	s.Equal(0, stats.ReadyIssues)
	s.Equal(0, stats.BlockedIssues)
}

func (s *testSuite) ucStatsAggregates() {
	s.resetDB()
	uc := s.issueUseCase()
	r := s.issueRepo()
	mk := func(id string, status types.Status) {
		iss := newTestIssue(id, string(status))
		iss.Status = status
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
	}
	mk("bd-ucst-o1", types.StatusOpen)
	mk("bd-ucst-o2", types.StatusOpen)
	mk("bd-ucst-ip", types.StatusInProgress)
	mk("bd-ucst-c", types.StatusClosed)

	stats, err := uc.GetStatistics(s.Ctx())
	s.Require().NoError(err)
	s.Equal(4, stats.TotalIssues)
	s.Equal(2, stats.OpenIssues)
	s.Equal(1, stats.InProgressIssues)
	s.Equal(1, stats.ClosedIssues)
}

func (s *testSuite) ucStatsReadyDerived() {
	s.resetDB()
	uc := s.issueUseCase()
	r := s.issueRepo()
	dr := s.depRepo()
	for _, id := range []string{"bd-ucsr-a", "bd-ucsr-b"} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, id), "tester", domain.InsertIssueOpts{}))
	}
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-ucsr-a", "bd-ucsr-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	stats, err := uc.GetStatistics(s.Ctx())
	s.Require().NoError(err)
	s.Equal(2, stats.OpenIssues)
	s.Equal(1, stats.BlockedIssues)
	s.Equal(1, stats.ReadyIssues, "UC must surface ready = open - blocked")
}

// ---------- DetectCycles UC ----------

func (s *testSuite) ucCyclesEmpty() {
	s.resetDB()
	out, err := s.depUseCase().DetectCycles(s.Ctx())
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) ucCyclesDetected() {
	s.resetDB()
	r := s.issueRepo()
	dr := s.depRepo()
	for _, id := range []string{"bd-uccy-a", "bd-uccy-b"} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, id), "tester", domain.InsertIssueOpts{}))
	}
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-uccy-a", "bd-uccy-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-uccy-b", "bd-uccy-a", types.DepBlocks), "tester", domain.DepInsertOpts{CycleValidated: true}))

	out, err := s.depUseCase().DetectCycles(s.Ctx())
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(len(out), 1)
	ids := cycleNodeIDs(out[0])
	s.Contains(ids, "bd-uccy-a")
	s.Contains(ids, "bd-uccy-b")
}

func (s *testSuite) ucCyclesAcyclic() {
	s.resetDB()
	r := s.issueRepo()
	dr := s.depRepo()
	for _, id := range []string{"bd-uccya-a", "bd-uccya-b", "bd-uccya-c"} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, id), "tester", domain.InsertIssueOpts{}))
	}
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-uccya-a", "bd-uccya-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-uccya-b", "bd-uccya-c", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := s.depUseCase().DetectCycles(s.Ctx())
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) ucReadyWorkPaginationRoundTrip() {
	s.resetDB()
	uc := s.issueUseCase()
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())
	const isoLabel = "uc-rdy-pag-isolate"
	const n = 5
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("bd-ucrdypag-p%d", i)
		iss := newTestIssue(id, fmt.Sprintf("p%d", i))
		iss.Priority = i
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
		s.Require().NoError(labelRepo.Insert(s.Ctx(), id, isoLabel, "tester", domain.LabelOpts{}))
	}
	base := types.WorkFilter{Labels: []string{isoLabel}, SortPolicy: types.SortPolicyPriority}

	page, err := uc.GetReadyWork(s.Ctx(), withOffsetLimit(base, 2, 2))
	s.Require().NoError(err)
	s.Require().Len(page.Items, 2, "Limit=2 must cap at 2 items")
	s.Equal("bd-ucrdypag-p3", page.Items[0].ID, "Offset=2 must skip p1+p2")
	s.Equal("bd-ucrdypag-p4", page.Items[1].ID, "second item must be p4")
	s.True(page.HasMore, "HasMore must propagate up from the repo (p5 remains)")
}
