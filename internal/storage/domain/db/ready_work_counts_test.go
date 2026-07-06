package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueGetReadyWorkWithCounts() {
	s.Run("ReturnsReadyIssues", s.readyCountsReturnsReady)
	s.Run("DependencyAndDependentCounts", s.readyCountsDepAndRDep)
	s.Run("CommentCount", s.readyCountsComment)
	s.Run("ParentPopulated", s.readyCountsParent)
	s.Run("ExcludesClosed", s.readyCountsExcludesClosed)
	s.Run("ExcludesPinned", s.readyCountsExcludesPinned)
	s.Run("ExcludesBlocked", s.readyCountsExcludesBlocked)
	s.Run("PriorityFilter", s.readyCountsPriorityFilter)
	s.Run("LabelHydration", s.readyCountsLabelHydration)
	s.Run("SortByPriority", s.readyCountsSortByPriority)
	s.Run("LimitRespected", s.readyCountsLimit)
	s.Run("CollisionAcrossTablesIsError", s.readyCountsCollision)
}

func (s *testSuite) readyCountsReturnsReady() {
	r := s.issueRepo()
	open := newTestIssue("bd-rdyc-r-open", "open")
	s.Require().NoError(r.Insert(s.Ctx(), open, "tester", domain.InsertIssueOpts{}))
	ip := newTestIssue("bd-rdyc-r-ip", "in progress")
	ip.Status = types.StatusInProgress
	s.Require().NoError(r.Insert(s.Ctx(), ip, "tester", domain.InsertIssueOpts{}))

	out, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	got := iwcIDs(out)
	s.Contains(got, "bd-rdyc-r-open")
	s.Contains(got, "bd-rdyc-r-ip")
}

func (s *testSuite) readyCountsDepAndRDep() {
	r := s.issueRepo()
	dep := s.depRepo()

	mid := newTestIssue("bd-rdyc-dr-mid", "mid")
	s.Require().NoError(r.Insert(s.Ctx(), mid, "tester", domain.InsertIssueOpts{}))
	a := newTestIssue("bd-rdyc-dr-a", "a")
	a.Status = types.StatusClosed
	s.Require().NoError(r.Insert(s.Ctx(), a, "tester", domain.InsertIssueOpts{}))
	b := newTestIssue("bd-rdyc-dr-b", "b")
	b.Status = types.StatusClosed
	s.Require().NoError(r.Insert(s.Ctx(), b, "tester", domain.InsertIssueOpts{}))
	c := newTestIssue("bd-rdyc-dr-c", "c")
	s.Require().NoError(r.Insert(s.Ctx(), c, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-rdyc-dr-mid", "bd-rdyc-dr-a", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-rdyc-dr-mid", "bd-rdyc-dr-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-rdyc-dr-c", "bd-rdyc-dr-mid", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	var midIWC *types.IssueWithCounts
	for _, iwc := range out.Items {
		if iwc.Issue.ID == "bd-rdyc-dr-mid" {
			midIWC = iwc
			break
		}
	}
	s.Require().NotNil(midIWC, "mid should be in ready work output")
	s.Equal(2, midIWC.DependencyCount)
	s.Equal(1, midIWC.DependentCount)
}

func (s *testSuite) readyCountsComment() {
	r := s.issueRepo()
	issue := newTestIssue("bd-rdyc-cmt-1", "commented")
	s.Require().NoError(r.Insert(s.Ctx(), issue, "tester", domain.InsertIssueOpts{}))
	for i := 0; i < 3; i++ {
		_, err := s.Runner().ExecContext(s.Ctx(),
			"INSERT INTO comments (id, issue_id, author, text) VALUES (UUID(), ?, ?, ?)",
			"bd-rdyc-cmt-1", "tester", "comment")
		s.Require().NoError(err)
	}

	out, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	var got *types.IssueWithCounts
	for _, iwc := range out.Items {
		if iwc.Issue.ID == "bd-rdyc-cmt-1" {
			got = iwc
			break
		}
	}
	s.Require().NotNil(got)
	s.Equal(3, got.CommentCount)
}

func (s *testSuite) readyCountsParent() {
	r := s.issueRepo()
	dep := s.depRepo()
	parent := newTestIssue("bd-rdyc-par-parent", "parent")
	s.Require().NoError(r.Insert(s.Ctx(), parent, "tester", domain.InsertIssueOpts{}))
	child := newTestIssue("bd-rdyc-par-child", "child")
	s.Require().NoError(r.Insert(s.Ctx(), child, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(),
		newDep("bd-rdyc-par-child", "bd-rdyc-par-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))

	out, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	var got *types.IssueWithCounts
	for _, iwc := range out.Items {
		if iwc.Issue.ID == "bd-rdyc-par-child" {
			got = iwc
			break
		}
	}
	s.Require().NotNil(got)
	s.Require().NotNil(got.Parent)
	s.Equal("bd-rdyc-par-parent", *got.Parent)
}

func (s *testSuite) readyCountsExcludesClosed() {
	r := s.issueRepo()
	closed := newTestIssue("bd-rdyc-cls-1", "closed")
	closed.Status = types.StatusClosed
	s.Require().NoError(r.Insert(s.Ctx(), closed, "tester", domain.InsertIssueOpts{}))

	out, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.NotContains(iwcIDs(out), "bd-rdyc-cls-1")
}

func (s *testSuite) readyCountsExcludesPinned() {
	r := s.issueRepo()
	pinned := newTestIssue("bd-rdyc-pin-1", "pinned")
	s.Require().NoError(r.Insert(s.Ctx(), pinned, "tester", domain.InsertIssueOpts{}))
	_, err := s.Runner().ExecContext(s.Ctx(), "UPDATE issues SET pinned = 1 WHERE id = ?", "bd-rdyc-pin-1")
	s.Require().NoError(err)

	out, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.NotContains(iwcIDs(out), "bd-rdyc-pin-1")
}

func (s *testSuite) readyCountsExcludesBlocked() {
	r := s.issueRepo()
	blocked := newTestIssue("bd-rdyc-blk-1", "blocked")
	s.Require().NoError(r.Insert(s.Ctx(), blocked, "tester", domain.InsertIssueOpts{}))
	_, err := s.Runner().ExecContext(s.Ctx(), "UPDATE issues SET is_blocked = 1 WHERE id = ?", "bd-rdyc-blk-1")
	s.Require().NoError(err)

	out, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.NotContains(iwcIDs(out), "bd-rdyc-blk-1")
}

func (s *testSuite) readyCountsPriorityFilter() {
	r := s.issueRepo()
	hi := newTestIssue("bd-rdyc-pr-hi", "hi")
	hi.Priority = 1
	s.Require().NoError(r.Insert(s.Ctx(), hi, "tester", domain.InsertIssueOpts{}))
	lo := newTestIssue("bd-rdyc-pr-lo", "lo")
	lo.Priority = 3
	s.Require().NoError(r.Insert(s.Ctx(), lo, "tester", domain.InsertIssueOpts{}))

	pri := 1
	out, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{Priority: &pri})
	s.Require().NoError(err)
	got := iwcIDs(out)
	s.Contains(got, "bd-rdyc-pr-hi")
	s.NotContains(got, "bd-rdyc-pr-lo")
}

func (s *testSuite) readyCountsLabelHydration() {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())
	issue := newTestIssue("bd-rdyc-lbl-1", "labeled")
	s.Require().NoError(r.Insert(s.Ctx(), issue, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-rdyc-lbl-1", "alpha", "tester", domain.LabelOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-rdyc-lbl-1", "beta", "tester", domain.LabelOpts{}))

	out, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	var got *types.IssueWithCounts
	for _, iwc := range out.Items {
		if iwc.Issue.ID == "bd-rdyc-lbl-1" {
			got = iwc
			break
		}
	}
	s.Require().NotNil(got)
	s.ElementsMatch([]string{"alpha", "beta"}, got.Issue.Labels)
}

func (s *testSuite) readyCountsSortByPriority() {
	r := s.issueRepo()
	lo := newTestIssue("bd-rdyc-srt-lo", "lo")
	lo.Priority = 3
	s.Require().NoError(r.Insert(s.Ctx(), lo, "tester", domain.InsertIssueOpts{}))
	hi := newTestIssue("bd-rdyc-srt-hi", "hi")
	hi.Priority = 1
	s.Require().NoError(r.Insert(s.Ctx(), hi, "tester", domain.InsertIssueOpts{}))
	mid := newTestIssue("bd-rdyc-srt-mid", "mid")
	mid.Priority = 2
	s.Require().NoError(r.Insert(s.Ctx(), mid, "tester", domain.InsertIssueOpts{}))

	out, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{SortPolicy: types.SortPolicyPriority})
	s.Require().NoError(err)
	got := iwcIDs(out)
	hiIdx, midIdx, loIdx := indexOf(got, "bd-rdyc-srt-hi"), indexOf(got, "bd-rdyc-srt-mid"), indexOf(got, "bd-rdyc-srt-lo")
	s.Require().GreaterOrEqual(hiIdx, 0)
	s.Require().GreaterOrEqual(midIdx, 0)
	s.Require().GreaterOrEqual(loIdx, 0)
	s.Less(hiIdx, midIdx)
	s.Less(midIdx, loIdx)
}

func (s *testSuite) readyCountsLimit() {
	r := s.issueRepo()
	for i := 0; i < 5; i++ {
		iss := newTestIssue("bd-rdyc-lim-"+string(rune('a'+i)), "x")
		iss.Priority = 1
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
	}
	pri := 1
	out, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{Priority: &pri, Limit: 3, SortPolicy: types.SortPolicyPriority})
	s.Require().NoError(err)
	s.Len(out.Items, 3)
}

func (s *testSuite) readyCountsCollision() {
	r := s.issueRepo()
	const id = "bd-rdyc-coll-1"
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, "perm"), "tester", domain.InsertIssueOpts{}))
	w := newTestIssue(id, "wisp")
	w.Ephemeral = false
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	_, err := r.GetReadyWorkWithCounts(s.Ctx(), types.WorkFilter{})
	s.Require().Error(err)
	s.Contains(err.Error(), "exists in both issues and wisps")
}
