package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueSQLRepositoryClose() {
	s.Run("ClosesOpenIssue", s.closeMutatesRow)
	s.Run("IdempotentOnAlreadyClosed", s.closeIdempotent)
	s.Run("RecomputesIsBlockedOnDependents", s.closeRecomputesIsBlocked)
	s.Run("MissingIDErrors", s.closeMissingID)
	s.Run("RoutesWisp", s.closeRoutesWisp)
}

func (s *testSuite) TestIssueSQLRepositoryGetNewlyUnblockedByClose() {
	s.Run("ReturnsDependentNowUnblocked", s.unblockedReturnsDependent)
	s.Run("OmitsDependentWithOtherOpenBlocker", s.unblockedOmitsWithOther)
	s.Run("OmitsAlreadyClosedDependent", s.unblockedOmitsClosed)
	s.Run("EmptyWhenNoDependents", s.unblockedEmpty)
}

func (s *testSuite) TestDependencySQLRepositoryIsBlocked() {
	s.Run("FalseWhenNoBlockers", s.isBlockedFalseWhenNoBlockers)
	s.Run("TrueWhenOpenBlocksEdge", s.isBlockedTrueOnBlocks)
	s.Run("FalseAfterBlockerClosed", s.isBlockedFalseAfterBlockerClosed)
	s.Run("TrueOnWaitsForEdge", s.isBlockedTrueOnWaitsFor)
	s.Run("TrueOnConditionalBlocksEdge", s.isBlockedTrueOnConditionalBlocks)
}

func (s *testSuite) closeMutatesRow() {
	s.seedIssueRow("bd-cl-row")
	r := NewIssueSQLRepository(s.Runner())

	res, err := r.Close(s.Ctx(), "bd-cl-row",
		domain.CloseRowParams{Reason: "done", Session: "sess-1"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.True(res.Updated)
	s.False(res.AlreadyClosed)
	s.False(res.IsWisp)

	var status, reason, session string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT status, close_reason, closed_by_session FROM issues WHERE id = ?", "bd-cl-row").
		Scan(&status, &reason, &session))
	s.Equal(string(types.StatusClosed), status)
	s.Equal("done", reason)
	s.Equal("sess-1", session)

	var evtCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-cl-row", string(types.EventClosed)).Scan(&evtCount))
	s.Equal(1, evtCount)
}

func (s *testSuite) closeIdempotent() {
	s.seedIssueRow("bd-cl-idem")
	r := NewIssueSQLRepository(s.Runner())

	_, err := r.Close(s.Ctx(), "bd-cl-idem",
		domain.CloseRowParams{Reason: "first"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	res, err := r.Close(s.Ctx(), "bd-cl-idem",
		domain.CloseRowParams{Reason: "second"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.False(res.Updated)
	s.True(res.AlreadyClosed)

	var reason string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT close_reason FROM issues WHERE id = ?", "bd-cl-idem").Scan(&reason))
	s.Equal("first", reason)

	var evtCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-cl-idem", string(types.EventClosed)).Scan(&evtCount))
	s.Equal(1, evtCount)
}

func (s *testSuite) closeRecomputesIsBlocked() {
	s.seedIssueRow("bd-cl-src")
	s.seedIssueRow("bd-cl-tgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-cl-src", "bd-cl-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().True(s.isBlocked("bd-cl-src"))

	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-cl-tgt",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	s.False(s.isBlocked("bd-cl-src"), "closing the blocker must clear cached is_blocked on dependent")
}

func (s *testSuite) closeMissingID() {
	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-cl-missing",
		domain.CloseRowParams{Reason: "noop"}, "tester", domain.IssueTableOpts{})
	s.Require().Error(err)
}

func (s *testSuite) closeRoutesWisp() {
	s.seedWispRow("bd-cl-wisp")
	r := NewIssueSQLRepository(s.Runner())

	res, err := r.Close(s.Ctx(), "bd-cl-wisp",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.True(res.Updated)
	s.True(res.IsWisp)

	var status string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT status FROM wisps WHERE id = ?", "bd-cl-wisp").Scan(&status))
	s.Equal(string(types.StatusClosed), status)
}

func (s *testSuite) unblockedReturnsDependent() {
	s.seedIssueRow("bd-un-dep")
	s.seedIssueRow("bd-un-blocker")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-un-dep", "bd-un-blocker", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-un-blocker",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	unblocked, err := r.GetNewlyUnblockedByClose(s.Ctx(), "bd-un-blocker")
	s.Require().NoError(err)
	s.Require().Len(unblocked, 1)
	s.Equal("bd-un-dep", unblocked[0].ID)
}

func (s *testSuite) unblockedOmitsWithOther() {
	s.seedIssueRow("bd-un2-dep")
	s.seedIssueRow("bd-un2-b1")
	s.seedIssueRow("bd-un2-b2")
	dep := s.depRepo()
	s.Require().NoError(dep.Insert(s.Ctx(),
		newDep("bd-un2-dep", "bd-un2-b1", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(),
		newDep("bd-un2-dep", "bd-un2-b2", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-un2-b1",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	unblocked, err := r.GetNewlyUnblockedByClose(s.Ctx(), "bd-un2-b1")
	s.Require().NoError(err)
	s.Empty(unblocked, "dependent still has bd-un2-b2 open as blocker")
}

func (s *testSuite) unblockedOmitsClosed() {
	s.seedIssueRow("bd-un3-dep")
	s.seedIssueRow("bd-un3-blocker")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-un3-dep", "bd-un3-blocker", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-un3-dep",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	_, err = r.Close(s.Ctx(), "bd-un3-blocker",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	unblocked, err := r.GetNewlyUnblockedByClose(s.Ctx(), "bd-un3-blocker")
	s.Require().NoError(err)
	s.Empty(unblocked, "already-closed dependents must not be returned")
}

func (s *testSuite) unblockedEmpty() {
	s.seedIssueRow("bd-un4-lonely")
	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-un4-lonely",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	unblocked, err := r.GetNewlyUnblockedByClose(s.Ctx(), "bd-un4-lonely")
	s.Require().NoError(err)
	s.Empty(unblocked)
}

func (s *testSuite) isBlockedFalseWhenNoBlockers() {
	s.seedIssueRow("bd-isb-free")
	blocked, blockers, err := s.depRepo().IsBlocked(s.Ctx(), "bd-isb-free", domain.DepListOpts{})
	s.Require().NoError(err)
	s.False(blocked)
	s.Empty(blockers)
}

func (s *testSuite) isBlockedTrueOnBlocks() {
	s.seedIssueRow("bd-isb-src")
	s.seedIssueRow("bd-isb-tgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-isb-src", "bd-isb-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	blocked, blockers, err := s.depRepo().IsBlocked(s.Ctx(), "bd-isb-src", domain.DepListOpts{})
	s.Require().NoError(err)
	s.True(blocked)
	s.Contains(blockers, "bd-isb-tgt")
}

func (s *testSuite) isBlockedFalseAfterBlockerClosed() {
	s.seedIssueRow("bd-isb-c-src")
	s.seedIssueRow("bd-isb-c-tgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-isb-c-src", "bd-isb-c-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-isb-c-tgt",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	blocked, _, err := s.depRepo().IsBlocked(s.Ctx(), "bd-isb-c-src", domain.DepListOpts{})
	s.Require().NoError(err)
	s.False(blocked)
}

func (s *testSuite) isBlockedTrueOnWaitsFor() {
	s.seedIssueRow("bd-isb-wf-src")
	s.seedIssueRow("bd-isb-wf-hard")
	s.seedIssueRow("bd-isb-wf-tgt")
	dep := s.depRepo()
	s.Require().NoError(dep.Insert(s.Ctx(),
		newDep("bd-isb-wf-src", "bd-isb-wf-hard", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(),
		newDep("bd-isb-wf-src", "bd-isb-wf-tgt", types.DepWaitsFor), "tester", domain.DepInsertOpts{}))

	blocked, blockers, err := dep.IsBlocked(s.Ctx(), "bd-isb-wf-src", domain.DepListOpts{})
	s.Require().NoError(err)
	s.True(blocked)
	s.Contains(blockers, "bd-isb-wf-hard")
	s.Contains(blockers, "bd-isb-wf-tgt (waits-for)")
}

func (s *testSuite) isBlockedTrueOnConditionalBlocks() {
	s.seedIssueRow("bd-isb-cb-src")
	s.seedIssueRow("bd-isb-cb-tgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-isb-cb-src", "bd-isb-cb-tgt", types.DepConditionalBlocks), "tester", domain.DepInsertOpts{}))

	blocked, blockers, err := s.depRepo().IsBlocked(s.Ctx(), "bd-isb-cb-src", domain.DepListOpts{})
	s.Require().NoError(err)
	s.True(blocked)
	s.Contains(blockers, "bd-isb-cb-tgt (conditional-blocks)")
}
