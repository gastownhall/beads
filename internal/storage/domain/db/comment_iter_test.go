package db

import (
	"context"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestCommentSQLRepositoryIter() {
	s.Run("IterByIssueID", func() {
		s.Run("StreamsInCreationOrder", s.commentIterOrdered)
		s.Run("EmptyIssueProducesEmptyIter", s.commentIterEmpty)
		s.Run("WispRouting", s.commentIterWispRouting)
		s.Run("WispOptDoesNotPickUpPerm", s.commentIterCrossRoutingIsolated)
	})
}

func (s *testSuite) commentIterOrdered() {
	s.seedIssueRow("bd-cmt-it-1")
	base := time.Now().UTC().Truncate(time.Second)
	s.seedComment("bd-cmt-it-1", "a", "second", base.Add(time.Second))
	s.seedComment("bd-cmt-it-1", "a", "first", base)
	s.seedComment("bd-cmt-it-1", "a", "third", base.Add(2*time.Second))

	it, err := s.commentRepo().IterByIssueID(s.Ctx(), "bd-cmt-it-1", domain.CommentOpts{})
	s.Require().NoError(err)
	defer it.Close() //nolint:errcheck

	var texts []string
	for it.Next(context.Background()) {
		c := it.Value()
		texts = append(texts, c.Text)
	}
	s.Require().NoError(it.Err())
	s.Equal([]string{"first", "second", "third"}, texts)
}

func (s *testSuite) commentIterEmpty() {
	s.seedIssueRow("bd-cmt-it-empty")
	it, err := s.commentRepo().IterByIssueID(s.Ctx(), "bd-cmt-it-empty", domain.CommentOpts{})
	s.Require().NoError(err)
	defer it.Close() //nolint:errcheck
	s.False(it.Next(context.Background()))
	s.NoError(it.Err())
}

func (s *testSuite) commentIterWispRouting() {
	s.seedWispRow("bd-cmt-it-wisp")
	base := time.Now().UTC().Truncate(time.Second)
	s.seedWispComment("bd-cmt-it-wisp", "a", "wisp first", base)
	s.seedWispComment("bd-cmt-it-wisp", "a", "wisp second", base.Add(time.Second))

	it, err := s.commentRepo().IterByIssueID(s.Ctx(), "bd-cmt-it-wisp", domain.CommentOpts{UseWispsTable: true})
	s.Require().NoError(err)
	defer it.Close() //nolint:errcheck

	var items []*types.Comment
	for it.Next(context.Background()) {
		items = append(items, it.Value())
	}
	s.Require().NoError(it.Err())
	s.Require().Len(items, 2)
	s.Equal("wisp first", items[0].Text)
	s.Equal("wisp second", items[1].Text)
}

func (s *testSuite) commentIterCrossRoutingIsolated() {
	s.seedIssueRow("bd-cmt-it-perm")
	s.seedWispRow("bd-cmt-it-w2")
	now := time.Now().UTC()
	s.seedComment("bd-cmt-it-perm", "a", "perm", now)
	s.seedWispComment("bd-cmt-it-w2", "a", "wisp", now)

	itPerm, err := s.commentRepo().IterByIssueID(s.Ctx(), "bd-cmt-it-w2", domain.CommentOpts{})
	s.Require().NoError(err)
	s.False(itPerm.Next(context.Background()), "perm-table iter for wisp ID must yield nothing")
	_ = itPerm.Close()

	itWisp, err := s.commentRepo().IterByIssueID(s.Ctx(), "bd-cmt-it-perm", domain.CommentOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.False(itWisp.Next(context.Background()), "wisp-table iter for perm ID must yield nothing")
	_ = itWisp.Close()
}
