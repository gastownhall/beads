package db

import (
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

// TestConditionalBlocksInsertOntoClosedTarget drives the domain/db direct
// writer (DependencySQLRepository.Insert) with a conditional-blocks edge onto
// an already-closed target on every issue/wisp route: the source is blocked
// exactly when the target's close reason is not a failure.
func (s *testSuite) TestConditionalBlocksInsertOntoClosedTarget() {
	routes := []struct {
		name                   string
		sourceWisp, targetWisp bool
	}{
		{"issue-to-issue", false, false},
		{"issue-to-wisp", false, true},
		{"wisp-to-issue", true, false},
		{"wisp-to-wisp", true, true},
	}
	reasons := []string{"completed successfully", "Rejected by review"}
	for ri, r := range routes {
		for oi, reason := range reasons {
			suffix := string(rune('0'+ri)) + string(rune('0'+oi))
			a, b := "bd-cbi-a"+suffix, "bd-cbi-b"+suffix
			targetTable, sourceTable := "issues", "issues"
			if r.targetWisp {
				s.seedWispRow(a)
				targetTable = "wisps"
			} else {
				s.seedIssueRow(a)
			}
			if r.sourceWisp {
				s.seedWispRow(b)
				sourceTable = "wisps"
			} else {
				s.seedIssueRow(b)
			}
			//nolint:gosec // G201: targetTable is "issues" or "wisps".
			_, err := s.Runner().ExecContext(s.Ctx(),
				"UPDATE "+targetTable+" SET status = 'closed', closed_at = CURRENT_TIMESTAMP, close_reason = ? WHERE id = ?", reason, a)
			s.Require().NoError(err)

			s.Require().NoError(s.depRepo().Insert(s.Ctx(),
				newDep(b, a, types.DepConditionalBlocks), "tester", domain.DepInsertOpts{UseWispsTable: r.sourceWisp}))

			var v int
			//nolint:gosec // G201: sourceTable is "issues" or "wisps".
			s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
				"SELECT is_blocked FROM "+sourceTable+" WHERE id = ?", b).Scan(&v))
			s.Equal(!types.IsFailureClose(reason), v != 0,
				"%s: insert onto target closed %q: stored is_blocked(%s)", r.name, reason, b)
		}
	}
}

// TestConditionalBlocksReasonEditFlipsDependent edits only a closed target's
// close reason through the repository Update: crossing between a non-failure
// and a failure reason flips the conditional dependent's stored bit in the
// same write, without touching the dependent's updated_at.
func (s *testSuite) TestConditionalBlocksReasonEditFlipsDependent() {
	s.seedIssueRow("bd-cbr-a")
	s.seedIssueRow("bd-cbr-b")
	_, err := s.Runner().ExecContext(s.Ctx(),
		"UPDATE issues SET status = 'closed', closed_at = CURRENT_TIMESTAMP, close_reason = 'completed successfully' WHERE id = 'bd-cbr-a'")
	s.Require().NoError(err)
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-cbr-b", "bd-cbr-a", types.DepConditionalBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().True(s.isBlocked("bd-cbr-b"), "success-closed conditional target must hold its dependent")

	updatedAt := func() string {
		var v string
		s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
			"SELECT CAST(updated_at AS CHAR) FROM issues WHERE id = 'bd-cbr-b'").Scan(&v))
		return v
	}
	before := updatedAt()

	issueRepo := NewIssueSQLRepository(s.Runner())
	for _, step := range []struct {
		reason      string
		wantBlocked bool
	}{
		{"failed downstream", false},
		{"done after all", true},
	} {
		s.Require().NoError(issueRepo.Update(s.Ctx(), "bd-cbr-a",
			map[string]any{"close_reason": step.reason}, "tester", domain.IssueTableOpts{}))
		s.Equal(step.wantBlocked, s.isBlocked("bd-cbr-b"), "reason edited to %q", step.reason)
		s.Equal(before, updatedAt(), "reason edit to %q must not touch the dependent's updated_at", step.reason)
	}
}
