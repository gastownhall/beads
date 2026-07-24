package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueUseCase_ReopenIssue() {
	s.Run("ReturnsReopenedIssue", s.uccReopenReturnsIssue)
	s.Run("AlreadyOpenReportsNotReopened", s.uccReopenAlreadyOpen)
	s.Run("EmptyIDErrors", s.uccReopenEmptyID)
	s.Run("EmptyActorErrors", s.uccReopenEmptyActor)
	s.Run("MissingIDErrors", s.uccReopenMissingID)
	s.Run("WispVariantRoutesToWispsTable", s.uccReopenWispRoutes)
	s.Run("ReasonRecordedAsComment", s.uccReopenReasonComment)
}

func (s *testSuite) uccReopenReturnsIssue() {
	s.seedIssueRow("bd-ucc-ro-1")
	uc := s.issueUseCase()
	_, err := uc.CloseIssue(s.Ctx(), "bd-ucc-ro-1",
		domain.CloseIssueParams{Reason: "done"}, "tester")
	s.Require().NoError(err)

	res, err := uc.ReopenIssue(s.Ctx(), "bd-ucc-ro-1",
		domain.ReopenIssueParams{Reason: "needed more work"}, "tester")
	s.Require().NoError(err)
	s.True(res.Reopened)
	s.Require().NotNil(res.Issue)
	s.Equal("bd-ucc-ro-1", res.Issue.ID)
	s.Equal(types.StatusOpen, res.Issue.Status)
}

func (s *testSuite) uccReopenAlreadyOpen() {
	s.seedIssueRow("bd-ucc-ro-2")
	uc := s.issueUseCase()

	res, err := uc.ReopenIssue(s.Ctx(), "bd-ucc-ro-2",
		domain.ReopenIssueParams{}, "tester")
	s.Require().NoError(err)
	s.False(res.Reopened)
	s.Require().NotNil(res.Issue)
	s.Equal(types.StatusOpen, res.Issue.Status)
}

func (s *testSuite) uccReopenEmptyID() {
	_, err := s.issueUseCase().ReopenIssue(s.Ctx(), "",
		domain.ReopenIssueParams{}, "tester")
	s.Require().Error(err)
}

func (s *testSuite) uccReopenEmptyActor() {
	s.seedIssueRow("bd-ucc-ro-noactor")
	_, err := s.issueUseCase().ReopenIssue(s.Ctx(), "bd-ucc-ro-noactor",
		domain.ReopenIssueParams{}, "")
	s.Require().Error(err)
}

func (s *testSuite) uccReopenMissingID() {
	_, err := s.issueUseCase().ReopenIssue(s.Ctx(), "bd-ucc-ro-missing",
		domain.ReopenIssueParams{}, "tester")
	s.Require().Error(err)
}

func (s *testSuite) uccReopenWispRoutes() {
	s.seedWispRow("bd-ucc-ro-wisp")
	uc := s.issueUseCase()
	_, err := uc.CloseWisp(s.Ctx(), "bd-ucc-ro-wisp",
		domain.CloseIssueParams{Reason: "done"}, "tester")
	s.Require().NoError(err)

	res, err := uc.ReopenWisp(s.Ctx(), "bd-ucc-ro-wisp",
		domain.ReopenIssueParams{}, "tester")
	s.Require().NoError(err)
	s.True(res.Reopened)
	s.Require().NotNil(res.Issue)
	s.Equal(types.StatusOpen, res.Issue.Status)

	var status string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT status FROM wisps WHERE id = ?", "bd-ucc-ro-wisp").Scan(&status))
	s.Equal(string(types.StatusOpen), status)
}

func (s *testSuite) uccReopenReasonComment() {
	s.seedIssueRow("bd-ucc-ro-cmt")
	uc := s.issueUseCase()
	_, err := uc.CloseIssue(s.Ctx(), "bd-ucc-ro-cmt",
		domain.CloseIssueParams{Reason: "done"}, "tester")
	s.Require().NoError(err)

	_, err = uc.ReopenIssue(s.Ctx(), "bd-ucc-ro-cmt",
		domain.ReopenIssueParams{Reason: "qa found a regression"}, "tester")
	s.Require().NoError(err)

	var comment string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT comment FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-ucc-ro-cmt", string(types.EventCommented)).Scan(&comment))
	s.Equal("qa found a regression", comment)
}
