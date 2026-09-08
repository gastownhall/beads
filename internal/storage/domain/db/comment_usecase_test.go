package db

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
)

func (s *testSuite) TestCommentUseCase() {
	s.Run("Issue", func() {
		s.Run("GetCommentsForIssueReturnsSlice", s.commentUCGetSlice)
		s.Run("CountCommentsForIssueReturnsCount", s.commentUCCount)
		s.Run("IterCommentsForIssueStreamsSameRows", s.commentUCIterMatches)
		s.Run("MissingIssueReturnsEmptySlice", s.commentUCGetMissingEmpty)
		s.Run("MissingIssueReturnsZeroCount", s.commentUCCountMissingZero)
		s.Run("EmptyIDRejected", s.commentUCEmptyIDs)
	})
	s.Run("Wisp", func() {
		s.Run("GetCommentsForWispRoutes", s.commentUCWispGet)
		s.Run("CountCommentsForWispRoutes", s.commentUCWispCount)
		s.Run("PermLookupOfWispCommentsReturnsEmpty", s.commentUCWispIsolated)
	})
}

func (s *testSuite) commentUseCase() domain.CommentUseCase {
	return domain.NewCommentUseCase(NewCommentSQLRepository(s.Runner()))
}

func (s *testSuite) commentUCGetSlice() {
	s.seedIssueRow("bd-cuc-get")
	base := time.Now().UTC().Truncate(time.Second)
	s.seedComment("bd-cuc-get", "a", "first", base)
	s.seedComment("bd-cuc-get", "a", "second", base.Add(time.Second))

	out, err := s.commentUseCase().GetCommentsForIssue(s.Ctx(), "bd-cuc-get")
	s.Require().NoError(err)
	s.Require().Len(out, 2)
	s.Equal("first", out[0].Text)
	s.Equal("second", out[1].Text)
}

func (s *testSuite) commentUCCount() {
	s.seedIssueRow("bd-cuc-cnt")
	now := time.Now().UTC()
	s.seedComment("bd-cuc-cnt", "a", "x", now)
	s.seedComment("bd-cuc-cnt", "a", "y", now)
	s.seedComment("bd-cuc-cnt", "a", "z", now)

	n, err := s.commentUseCase().CountCommentsForIssue(s.Ctx(), "bd-cuc-cnt")
	s.Require().NoError(err)
	s.Equal(int64(3), n)
}

func (s *testSuite) commentUCIterMatches() {
	s.seedIssueRow("bd-cuc-iter")
	base := time.Now().UTC().Truncate(time.Second)
	s.seedComment("bd-cuc-iter", "a", "alpha", base)
	s.seedComment("bd-cuc-iter", "a", "beta", base.Add(time.Second))

	list, err := s.commentUseCase().GetCommentsForIssue(s.Ctx(), "bd-cuc-iter")
	s.Require().NoError(err)

	it, err := s.commentUseCase().IterCommentsForIssue(s.Ctx(), "bd-cuc-iter")
	s.Require().NoError(err)
	defer it.Close() //nolint:errcheck

	streamed := []string{}
	for it.Next(context.Background()) {
		streamed = append(streamed, it.Value().Text)
	}
	s.Require().NoError(it.Err())
	s.Require().Len(streamed, len(list))
	s.Equal("alpha", streamed[0])
	s.Equal("beta", streamed[1])
}

func (s *testSuite) commentUCGetMissingEmpty() {
	out, err := s.commentUseCase().GetCommentsForIssue(s.Ctx(), "bd-cuc-ghost")
	s.Require().NoError(err)
	s.Empty(out, "missing issue yields empty slice, not error")
}

func (s *testSuite) commentUCCountMissingZero() {
	n, err := s.commentUseCase().CountCommentsForIssue(s.Ctx(), "bd-cuc-ghost-cnt")
	s.Require().NoError(err)
	s.Equal(int64(0), n)
}

func (s *testSuite) commentUCEmptyIDs() {
	uc := s.commentUseCase()

	_, err := uc.GetCommentsForIssue(s.Ctx(), "")
	s.Require().Error(err)
	s.Contains(err.Error(), "id must not be empty")

	_, err = uc.CountCommentsForIssue(s.Ctx(), "")
	s.Require().Error(err)

	_, err = uc.IterCommentsForIssue(s.Ctx(), "")
	s.Require().Error(err)

	_, err = uc.GetCommentsForWisp(s.Ctx(), "")
	s.Require().Error(err)

	_, err = uc.CountCommentsForWisp(s.Ctx(), "")
	s.Require().Error(err)

	_, err = uc.IterCommentsForWisp(s.Ctx(), "")
	s.Require().Error(err)
}

func (s *testSuite) commentUCWispGet() {
	s.seedWispRow("bd-cuc-wisp-get")
	now := time.Now().UTC()
	s.seedWispComment("bd-cuc-wisp-get", "a", "wisp comment", now)

	out, err := s.commentUseCase().GetCommentsForWisp(s.Ctx(), "bd-cuc-wisp-get")
	s.Require().NoError(err)
	s.Require().Len(out, 1)
	s.Equal("wisp comment", out[0].Text)
}

func (s *testSuite) commentUCWispCount() {
	s.seedWispRow("bd-cuc-wisp-cnt")
	now := time.Now().UTC()
	s.seedWispComment("bd-cuc-wisp-cnt", "a", "x", now)
	s.seedWispComment("bd-cuc-wisp-cnt", "a", "y", now)

	n, err := s.commentUseCase().CountCommentsForWisp(s.Ctx(), "bd-cuc-wisp-cnt")
	s.Require().NoError(err)
	s.Equal(int64(2), n)
}

func (s *testSuite) commentUCWispIsolated() {
	s.seedWispRow("bd-cuc-wisp-iso")
	s.seedWispComment("bd-cuc-wisp-iso", "a", "wisp only", time.Now().UTC())

	out, err := s.commentUseCase().GetCommentsForIssue(s.Ctx(), "bd-cuc-wisp-iso")
	s.Require().NoError(err)
	s.Empty(out, "perm-table lookup must not pick up wisp comments")
}
