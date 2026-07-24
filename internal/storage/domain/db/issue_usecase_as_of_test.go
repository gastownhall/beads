package db

func (s *testSuite) TestIssueUseCaseAsOf() {
	s.Run("DelegatesToRepoAtRef", s.issueUCAsOfHistorical)
	s.Run("EmptyIDRejected", s.issueUCAsOfEmptyID)
	s.Run("PropagatesRepoError", s.issueUCAsOfBadRef)
}

func (s *testSuite) issueUCAsOfHistorical() {
	s.seedIssueRow("bd-iuc-asof")
	hash := s.doltCommit("seed bd-iuc-asof")

	_, err := s.Runner().ExecContext(s.Ctx(),
		"UPDATE issues SET title = ? WHERE id = ?", "after", "bd-iuc-asof")
	s.Require().NoError(err)
	_ = s.doltCommit("modify bd-iuc-asof")

	out, err := s.issueUseCase().AsOf(s.Ctx(), "bd-iuc-asof", hash)
	s.Require().NoError(err)
	s.Require().NotNil(out)
	s.Equal("seed", out.Title)
}

func (s *testSuite) issueUCAsOfEmptyID() {
	_, err := s.issueUseCase().AsOf(s.Ctx(), "", "HEAD")
	s.Require().Error(err)
	s.Contains(err.Error(), "id must not be empty")
}

func (s *testSuite) issueUCAsOfBadRef() {
	_, err := s.issueUseCase().AsOf(s.Ctx(), "bd-iuc-asof-x", "'; DROP TABLE issues; --")
	s.Require().Error(err)
	s.Contains(err.Error(), "invalid ref")
}
