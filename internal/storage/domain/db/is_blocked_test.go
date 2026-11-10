package db

import (
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIsBlockedParity() {
	s.Run("DeleteBlocksEdgeUnblocksSource", s.ibDeleteBlocksUnblocks)
	s.Run("DeleteParentChildEdgeUnblocksChild", s.ibDeleteParentChildUnblocks)
	s.Run("UpdateStatusClosedUnblocksDependents", s.ibUpdateCloseUnblocks)
	s.Run("UpdateStatusReopenReblocksDependents", s.ibUpdateReopenReblocks)
	s.Run("ReparentAwayFromBlockedParentUnblocksChild", s.ibReparentUnblocks)
	s.Run("ClaimDoesNotChangeIsBlocked", s.ibClaimDoesNotChange)
}

func (s *testSuite) isBlocked(id string) bool {
	var v int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT is_blocked FROM issues WHERE id = ?", id).Scan(&v))
	return v != 0
}

func (s *testSuite) ibDeleteBlocksUnblocks() {
	s.seedIssueRow("bd-ib-a")
	s.seedIssueRow("bd-ib-b")
	r := s.depRepo()

	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-ib-a", "bd-ib-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().True(s.isBlocked("bd-ib-a"), "source must be blocked after blocks-edge insert")

	res, err := r.Delete(s.Ctx(), "bd-ib-a", "bd-ib-b", "tester", domain.DepInsertOpts{})
	s.Require().NoError(err)
	s.Require().True(res.Found)
	s.Equal(types.DepBlocks, res.Type)
	s.False(s.isBlocked("bd-ib-a"), "source must be unblocked once last blocks-edge is removed")
}

func (s *testSuite) ibDeleteParentChildUnblocks() {
	s.seedIssueRow("bd-ib-pc-parent")
	s.seedIssueRow("bd-ib-pc-child")
	s.seedIssueRow("bd-ib-pc-blocker")
	r := s.depRepo()

	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-ib-pc-parent", "bd-ib-pc-blocker", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-ib-pc-child", "bd-ib-pc-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))
	s.Require().True(s.isBlocked("bd-ib-pc-parent"))
	s.Require().True(s.isBlocked("bd-ib-pc-child"), "child must inherit parent's blocked state")

	res, err := r.Delete(s.Ctx(), "bd-ib-pc-child", "bd-ib-pc-parent", "tester", domain.DepInsertOpts{})
	s.Require().NoError(err)
	s.Require().True(res.Found)
	s.False(s.isBlocked("bd-ib-pc-child"), "child must be unblocked once severed from blocked parent")
	s.True(s.isBlocked("bd-ib-pc-parent"), "parent still has its own blocker")
}

func (s *testSuite) ibUpdateCloseUnblocks() {
	s.seedIssueRow("bd-ib-up-src")
	s.seedIssueRow("bd-ib-up-tgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-ib-up-src", "bd-ib-up-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().True(s.isBlocked("bd-ib-up-src"))

	issueRepo := NewIssueSQLRepository(s.Runner())
	_, err := issueRepo.Close(s.Ctx(), "bd-ib-up-tgt", domain.CloseRowParams{}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	s.False(s.isBlocked("bd-ib-up-src"), "closing the blocker must unblock its dependent")
}

func (s *testSuite) ibUpdateReopenReblocks() {
	s.seedIssueRow("bd-ib-re-src")
	s.seedIssueRow("bd-ib-re-tgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-ib-re-src", "bd-ib-re-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	issueRepo := NewIssueSQLRepository(s.Runner())
	_, err := issueRepo.Close(s.Ctx(), "bd-ib-re-tgt", domain.CloseRowParams{}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Require().False(s.isBlocked("bd-ib-re-src"))

	_, err = issueRepo.Reopen(s.Ctx(), "bd-ib-re-tgt", domain.ReopenRowParams{}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.True(s.isBlocked("bd-ib-re-src"), "reopening the blocker must re-block its dependent")
}

func (s *testSuite) ibReparentUnblocks() {
	s.seedIssueRow("bd-ib-rp-blockedparent")
	s.seedIssueRow("bd-ib-rp-cleanparent")
	s.seedIssueRow("bd-ib-rp-child")
	s.seedIssueRow("bd-ib-rp-blocker")
	r := s.depRepo()

	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-ib-rp-blockedparent", "bd-ib-rp-blocker", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-ib-rp-child", "bd-ib-rp-blockedparent", types.DepParentChild), "tester", domain.DepInsertOpts{}))
	s.Require().True(s.isBlocked("bd-ib-rp-child"))

	depUC := domain.NewDependencyUseCase(r)
	s.Require().NoError(depUC.Reparent(s.Ctx(), "bd-ib-rp-child", "bd-ib-rp-cleanparent", "tester"))

	s.False(s.isBlocked("bd-ib-rp-child"), "child must be unblocked after reparenting onto a clean parent")
}

func (s *testSuite) ibClaimDoesNotChange() {
	s.seedIssueRow("bd-ib-cl-src")
	s.seedIssueRow("bd-ib-cl-blocker")
	s.seedIssueRow("bd-ib-cl-free")
	r := s.depRepo()

	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-ib-cl-src", "bd-ib-cl-blocker", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().True(s.isBlocked("bd-ib-cl-src"))
	s.Require().False(s.isBlocked("bd-ib-cl-free"))

	issueRepo := NewIssueSQLRepository(s.Runner())
	res, err := issueRepo.Claim(s.Ctx(), "bd-ib-cl-free", "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Require().True(res.Updated)

	s.True(s.isBlocked("bd-ib-cl-src"), "blocked dependent must remain blocked across an unrelated claim")
	s.False(s.isBlocked("bd-ib-cl-free"), "claimed issue (open->in_progress) must not become blocked")
	s.False(s.isBlocked("bd-ib-cl-blocker"), "untouched issue must stay unblocked")
}

// The direct is_blocked mark a dependency insert performs must answer the
// conditional-blocks rule when the target is already closed (GH#5577): a
// success-closed precondition still blocks the fallback, a failure-closed one
// does not.
func (s *testSuite) TestConditionalBlocksDirectMarkClosedTarget() {
	s.Run("SuccessCloseMarksBlocked", func() {
		s.seedIssueRow("bd-cb-closed-src")
		s.seedIssueRow("bd-cb-closed-tgt")
		issueRepo := NewIssueSQLRepository(s.Runner())
		_, err := issueRepo.Close(s.Ctx(), "bd-cb-closed-tgt",
			domain.CloseRowParams{Reason: "completed successfully"}, "tester", domain.IssueTableOpts{})
		s.Require().NoError(err)

		s.Require().NoError(s.depRepo().Insert(s.Ctx(),
			newDep("bd-cb-closed-src", "bd-cb-closed-tgt", types.DepConditionalBlocks), "tester", domain.DepInsertOpts{}))
		s.True(s.isBlocked("bd-cb-closed-src"),
			"a conditional-blocks edge onto a success-closed target must mark its source blocked")
	})
	s.Run("FailureCloseLeavesFree", func() {
		s.seedIssueRow("bd-cb-failed-src")
		s.seedIssueRow("bd-cb-failed-tgt")
		issueRepo := NewIssueSQLRepository(s.Runner())
		_, err := issueRepo.Close(s.Ctx(), "bd-cb-failed-tgt",
			domain.CloseRowParams{Reason: "failed: rejected by reviewer"}, "tester", domain.IssueTableOpts{})
		s.Require().NoError(err)

		s.Require().NoError(s.depRepo().Insert(s.Ctx(),
			newDep("bd-cb-failed-src", "bd-cb-failed-tgt", types.DepConditionalBlocks), "tester", domain.DepInsertOpts{}))
		s.False(s.isBlocked("bd-cb-failed-src"),
			"a conditional-blocks edge onto a failure-closed target must leave its source free")
	})
}

// A close-reason edit that flips the failure classification must recompute the
// conditional dependents even though no status changes — and without moving
// the dependent's updated_at (GH#5577).
func (s *testSuite) TestConditionalBlocksReasonEditRecomputes() {
	s.seedIssueRow("bd-cb-reason-src")
	s.seedIssueRow("bd-cb-reason-tgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-cb-reason-src", "bd-cb-reason-tgt", types.DepConditionalBlocks), "tester", domain.DepInsertOpts{}))
	s.True(s.isBlocked("bd-cb-reason-src"))

	issueRepo := NewIssueSQLRepository(s.Runner())
	_, err := issueRepo.Close(s.Ctx(), "bd-cb-reason-tgt",
		domain.CloseRowParams{Reason: "completed successfully"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.True(s.isBlocked("bd-cb-reason-src"), "a success close keeps the fallback blocked")

	var updatedBefore time.Time
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT updated_at FROM issues WHERE id = ?", "bd-cb-reason-src").Scan(&updatedBefore))

	s.Require().NoError(issueRepo.Update(s.Ctx(), "bd-cb-reason-tgt",
		map[string]any{"close_reason": "failed: flaky in CI"}, "tester", domain.IssueTableOpts{}))
	s.False(s.isBlocked("bd-cb-reason-src"),
		"a reason edit to a failure classification must release the fallback")

	s.Require().NoError(issueRepo.Update(s.Ctx(), "bd-cb-reason-tgt",
		map[string]any{"close_reason": "completed successfully"}, "tester", domain.IssueTableOpts{}))
	s.True(s.isBlocked("bd-cb-reason-src"),
		"a reason edit back to a success classification must re-block the fallback")

	var updatedAfter time.Time
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT updated_at FROM issues WHERE id = ?", "bd-cb-reason-src").Scan(&updatedAfter))
	s.Equal(updatedBefore.Unix(), updatedAfter.Unix(),
		"recompute is derived-state maintenance; the dependent's updated_at must not move")
}
