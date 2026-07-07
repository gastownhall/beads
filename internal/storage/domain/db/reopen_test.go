package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueSQLRepositoryReopen() {
	s.Run("ReopensClosedIssue", s.reopenMutatesRow)
	s.Run("IdempotentOnAlreadyOpen", s.reopenIdempotent)
	s.Run("RecomputesIsBlockedOnDependents", s.reopenRecomputesIsBlocked)
	s.Run("MissingIDErrors", s.reopenMissingID)
	s.Run("RoutesWisp", s.reopenRoutesWisp)
	s.Run("AppendsCommentOnReason", s.reopenAppendsComment)
	s.Run("NoCommentOnEmptyReason", s.reopenNoCommentEmptyReason)
}

func (s *testSuite) reopenMutatesRow() {
	s.seedIssueRow("bd-ro-row")
	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-ro-row",
		domain.CloseRowParams{Reason: "done", Session: "sess-1"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	res, err := r.Reopen(s.Ctx(), "bd-ro-row",
		domain.ReopenRowParams{Reason: "not really done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.True(res.Updated)
	s.False(res.AlreadyOpen)
	s.False(res.IsWisp)

	var status, reason, session string
	var closedAt *string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT status, closed_at, close_reason, closed_by_session FROM issues WHERE id = ?", "bd-ro-row").
		Scan(&status, &closedAt, &reason, &session))
	s.Equal(string(types.StatusOpen), status)
	s.Nil(closedAt, "closed_at must be cleared on reopen")
	s.Equal("", reason)
	s.Equal("", session)

	var evtCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-ro-row", string(types.EventReopened)).Scan(&evtCount))
	s.Equal(1, evtCount)
}

func (s *testSuite) reopenIdempotent() {
	s.seedIssueRow("bd-ro-idem")
	r := NewIssueSQLRepository(s.Runner())

	res, err := r.Reopen(s.Ctx(), "bd-ro-idem",
		domain.ReopenRowParams{}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.False(res.Updated)
	s.True(res.AlreadyOpen)

	var evtCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-ro-idem", string(types.EventReopened)).Scan(&evtCount))
	s.Equal(0, evtCount, "must not record event when nothing changed")
}

func (s *testSuite) reopenRecomputesIsBlocked() {
	s.seedIssueRow("bd-ro-src")
	s.seedIssueRow("bd-ro-tgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-ro-src", "bd-ro-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-ro-tgt",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.False(s.isBlocked("bd-ro-src"))

	_, err = r.Reopen(s.Ctx(), "bd-ro-tgt",
		domain.ReopenRowParams{}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.True(s.isBlocked("bd-ro-src"), "reopening the blocker must re-block the dependent")
}

func (s *testSuite) reopenMissingID() {
	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Reopen(s.Ctx(), "bd-ro-missing",
		domain.ReopenRowParams{}, "tester", domain.IssueTableOpts{})
	s.Require().Error(err)
}

func (s *testSuite) reopenRoutesWisp() {
	s.seedWispRow("bd-ro-wisp")
	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-ro-wisp",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)

	res, err := r.Reopen(s.Ctx(), "bd-ro-wisp",
		domain.ReopenRowParams{}, "tester", domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.True(res.Updated)
	s.True(res.IsWisp)

	var status string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT status FROM wisps WHERE id = ?", "bd-ro-wisp").Scan(&status))
	s.Equal(string(types.StatusOpen), status)

	var evtCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ? AND event_type = ?",
		"bd-ro-wisp", string(types.EventReopened)).Scan(&evtCount))
	s.Equal(1, evtCount)
}

func (s *testSuite) reopenAppendsComment() {
	s.seedIssueRow("bd-ro-cmt")
	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-ro-cmt",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	_, err = r.Reopen(s.Ctx(), "bd-ro-cmt",
		domain.ReopenRowParams{Reason: "regression spotted"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	var comment string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT comment FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-ro-cmt", string(types.EventCommented)).Scan(&comment))
	s.Equal("regression spotted", comment)
}

func (s *testSuite) reopenNoCommentEmptyReason() {
	s.seedIssueRow("bd-ro-nocmt")
	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-ro-nocmt",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	_, err = r.Reopen(s.Ctx(), "bd-ro-nocmt",
		domain.ReopenRowParams{}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	var cmtCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-ro-nocmt", string(types.EventCommented)).Scan(&cmtCount))
	s.Equal(0, cmtCount)
}
