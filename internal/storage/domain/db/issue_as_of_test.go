package db

import (
	"errors"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
)

func (s *testSuite) TestIssueSQLRepositoryAsOf() {
	s.Run("ReturnsRowAsOfCommit", s.asOfReturnsHistoricalRow)
	s.Run("CurrentReadStillReflectsLatest", s.asOfCurrentStillLatest)
	s.Run("MissingIssueReturnsErrNotFound", s.asOfMissingIssue)
	s.Run("InvalidRefRejected", s.asOfInvalidRef)
	s.Run("EmptyRefRejected", s.asOfEmptyRef)
}

func (s *testSuite) doltCommit(msg string) string {
	_, err := s.Runner().ExecContext(s.Ctx(), "CALL DOLT_ADD('-A')")
	s.Require().NoError(err)
	_, err = s.Runner().ExecContext(s.Ctx(), "CALL DOLT_COMMIT('-m', ?, '--allow-empty')", msg)
	s.Require().NoError(err)
	var hash string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(), "SELECT HASHOF('HEAD')").Scan(&hash))
	return hash
}

func (s *testSuite) asOfReturnsHistoricalRow() {
	r := s.issueRepo()
	in := newTestIssue("bd-asof-1", "original")
	s.Require().NoError(r.Insert(s.Ctx(), in, "tester", domain.InsertIssueOpts{}))

	initialHash := s.doltCommit("seed bd-asof-1")

	s.Require().NoError(r.Update(s.Ctx(), "bd-asof-1",
		map[string]any{"title": "modified"}, "tester", domain.IssueTableOpts{}))
	_ = s.doltCommit("modify bd-asof-1")

	old, err := r.AsOf(s.Ctx(), "bd-asof-1", initialHash)
	s.Require().NoError(err)
	s.Require().NotNil(old)
	s.Equal("original", old.Title)
}

func (s *testSuite) asOfCurrentStillLatest() {
	r := s.issueRepo()
	in := newTestIssue("bd-asof-cur", "v1")
	s.Require().NoError(r.Insert(s.Ctx(), in, "tester", domain.InsertIssueOpts{}))
	hash := s.doltCommit("seed bd-asof-cur")

	s.Require().NoError(r.Update(s.Ctx(), "bd-asof-cur",
		map[string]any{"title": "v2"}, "tester", domain.IssueTableOpts{}))
	_ = s.doltCommit("modify bd-asof-cur")

	current, err := r.Get(s.Ctx(), "bd-asof-cur", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Require().NotNil(current)
	s.Equal("v2", current.Title)

	historical, err := r.AsOf(s.Ctx(), "bd-asof-cur", hash)
	s.Require().NoError(err)
	s.Require().NotNil(historical)
	s.Equal("v1", historical.Title)
}

func (s *testSuite) asOfMissingIssue() {
	hash := s.doltCommit("asof-missing baseline")
	_, err := s.issueRepo().AsOf(s.Ctx(), "bd-asof-nope", hash)
	s.Require().Error(err)
	s.True(errors.Is(err, storage.ErrNotFound), "missing row at ref must surface storage.ErrNotFound")
}

func (s *testSuite) asOfInvalidRef() {
	_, err := s.issueRepo().AsOf(s.Ctx(), "bd-anything", "'; DROP TABLE issues; --")
	s.Require().Error(err)
	s.Contains(err.Error(), "invalid ref")
}

func (s *testSuite) asOfEmptyRef() {
	_, err := s.issueRepo().AsOf(s.Ctx(), "bd-anything", "")
	s.Require().Error(err)
	s.Contains(err.Error(), "ref cannot be empty")
}
