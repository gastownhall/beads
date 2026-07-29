package db

import (
	"time"

	"github.com/google/uuid"

	"github.com/steveyegge/beads/internal/storage/domain"
)

func (s *testSuite) TestCommentSQLRepository() {
	s.Run("CountsByIssueIDs", func() {
		s.Run("EmptySliceReturnsEmptyMap", s.commentCountsEmpty)
		s.Run("AggregatesPerIssue", s.commentCountsAggregates)
		s.Run("MissingIssuesAbsentFromResult", s.commentCountsMissingAbsent)
	})
	s.Run("ListByIssueIDs", func() {
		s.Run("EmptySliceReturnsEmptyMap", s.commentListEmpty)
		s.Run("OrdersByCreatedAtAscending", s.commentListOrdered)
		s.Run("GroupsByIssueID", s.commentListGrouped)
		s.Run("FieldsRoundTrip", s.commentListRoundTrip)
		s.Run("MissingIssueAbsent", s.commentListMissingAbsent)
	})
	s.Run("Wisp", func() {
		s.Run("CountsRouteToWispComments", s.commentWispCountsRouting)
		s.Run("ListRoutesToWispComments", s.commentWispListRouting)
		s.Run("IsolatedFromPermanent", s.commentWispIsolated)
	})
	s.Run("Insert", func() {
		s.Run("EmptyIssueIDReturnsError", s.commentInsertEmptyID)
		s.Run("MissingIssueReturnsError", s.commentInsertMissingIssue)
		s.Run("MissingWispReturnsError", s.commentInsertMissingWisp)
		s.Run("PopulatesReturnedComment", s.commentInsertPopulatesReturn)
		s.Run("RoundTripsThroughList", s.commentInsertRoundTrip)
		s.Run("RoutesToWispComments", s.commentInsertWispRouting)
	})
}

func (s *testSuite) commentRepo() domain.CommentSQLRepository {
	return NewCommentSQLRepository(s.Runner())
}

func (s *testSuite) seedComment(issueID, author, text string, createdAt time.Time) string {
	id := uuid.Must(uuid.NewV7()).String()
	_, err := s.Runner().ExecContext(s.Ctx(), `
		INSERT INTO comments (id, issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, id, issueID, author, text, createdAt.UTC())
	s.Require().NoError(err)
	return id
}

func (s *testSuite) seedWispComment(issueID, author, text string, createdAt time.Time) string {
	id := uuid.Must(uuid.NewV7()).String()
	_, err := s.Runner().ExecContext(s.Ctx(), `
		INSERT INTO wisp_comments (id, issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, id, issueID, author, text, createdAt.UTC())
	s.Require().NoError(err)
	return id
}

func (s *testSuite) commentCountsEmpty() {
	out, err := s.commentRepo().CountsByIssueIDs(s.Ctx(), nil, domain.CommentOpts{})
	s.Require().NoError(err)
	s.NotNil(out)
	s.Empty(out)
}

func (s *testSuite) commentCountsAggregates() {
	s.seedIssueRow("bd-cmt-cnt-1")
	s.seedIssueRow("bd-cmt-cnt-2")
	now := time.Now().UTC()
	s.seedComment("bd-cmt-cnt-1", "alice", "first", now)
	s.seedComment("bd-cmt-cnt-1", "alice", "second", now.Add(time.Second))
	s.seedComment("bd-cmt-cnt-1", "bob", "third", now.Add(2*time.Second))
	s.seedComment("bd-cmt-cnt-2", "alice", "only", now)

	out, err := s.commentRepo().CountsByIssueIDs(s.Ctx(), []string{"bd-cmt-cnt-1", "bd-cmt-cnt-2"}, domain.CommentOpts{})
	s.Require().NoError(err)
	s.Equal(3, out["bd-cmt-cnt-1"])
	s.Equal(1, out["bd-cmt-cnt-2"])
}

func (s *testSuite) commentCountsMissingAbsent() {
	s.seedIssueRow("bd-cmt-cnt-real")
	s.seedComment("bd-cmt-cnt-real", "alice", "x", time.Now().UTC())

	out, err := s.commentRepo().CountsByIssueIDs(s.Ctx(), []string{"bd-cmt-cnt-real", "bd-cmt-cnt-ghost"}, domain.CommentOpts{})
	s.Require().NoError(err)
	s.Equal(1, out["bd-cmt-cnt-real"])
	_, present := out["bd-cmt-cnt-ghost"]
	s.False(present, "issues with zero comments should not appear in the count map")
}

func (s *testSuite) commentListEmpty() {
	out, err := s.commentRepo().ListByIssueIDs(s.Ctx(), nil, domain.CommentOpts{})
	s.Require().NoError(err)
	s.NotNil(out)
	s.Empty(out)
}

func (s *testSuite) commentListOrdered() {
	s.seedIssueRow("bd-cmt-ord")
	base := time.Now().UTC().Truncate(time.Second)
	s.seedComment("bd-cmt-ord", "a", "third", base.Add(2*time.Second))
	s.seedComment("bd-cmt-ord", "a", "first", base)
	s.seedComment("bd-cmt-ord", "a", "second", base.Add(time.Second))

	out, err := s.commentRepo().ListByIssueIDs(s.Ctx(), []string{"bd-cmt-ord"}, domain.CommentOpts{})
	s.Require().NoError(err)
	s.Require().Len(out["bd-cmt-ord"], 3)
	s.Equal("first", out["bd-cmt-ord"][0].Text)
	s.Equal("second", out["bd-cmt-ord"][1].Text)
	s.Equal("third", out["bd-cmt-ord"][2].Text)
}

func (s *testSuite) commentListGrouped() {
	s.seedIssueRow("bd-cmt-grp-1")
	s.seedIssueRow("bd-cmt-grp-2")
	now := time.Now().UTC()
	s.seedComment("bd-cmt-grp-1", "a", "one-a", now)
	s.seedComment("bd-cmt-grp-1", "b", "one-b", now.Add(time.Second))
	s.seedComment("bd-cmt-grp-2", "a", "two-a", now)

	out, err := s.commentRepo().ListByIssueIDs(s.Ctx(), []string{"bd-cmt-grp-1", "bd-cmt-grp-2"}, domain.CommentOpts{})
	s.Require().NoError(err)
	s.Len(out["bd-cmt-grp-1"], 2)
	s.Len(out["bd-cmt-grp-2"], 1)
	s.Equal("two-a", out["bd-cmt-grp-2"][0].Text)
}

func (s *testSuite) commentListRoundTrip() {
	s.seedIssueRow("bd-cmt-rt")
	created := time.Now().UTC().Truncate(time.Second)
	id := s.seedComment("bd-cmt-rt", "alice", "hello world", created)

	out, err := s.commentRepo().ListByIssueIDs(s.Ctx(), []string{"bd-cmt-rt"}, domain.CommentOpts{})
	s.Require().NoError(err)
	s.Require().Len(out["bd-cmt-rt"], 1)
	c := out["bd-cmt-rt"][0]
	s.Equal(id, c.ID)
	s.Equal("bd-cmt-rt", c.IssueID)
	s.Equal("alice", c.Author)
	s.Equal("hello world", c.Text)
	s.Equal(created.Unix(), c.CreatedAt.Unix())
}

func (s *testSuite) commentListMissingAbsent() {
	s.seedIssueRow("bd-cmt-real")
	s.seedComment("bd-cmt-real", "a", "x", time.Now().UTC())

	out, err := s.commentRepo().ListByIssueIDs(s.Ctx(), []string{"bd-cmt-real", "bd-cmt-ghost"}, domain.CommentOpts{})
	s.Require().NoError(err)
	s.Len(out["bd-cmt-real"], 1)
	_, present := out["bd-cmt-ghost"]
	s.False(present, "missing issue IDs should not appear in the result map")
}

func (s *testSuite) commentWispCountsRouting() {
	s.seedWispRow("bd-cmt-wisp-cnt")
	now := time.Now().UTC()
	s.seedWispComment("bd-cmt-wisp-cnt", "alice", "a", now)
	s.seedWispComment("bd-cmt-wisp-cnt", "alice", "b", now.Add(time.Second))

	out, err := s.commentRepo().CountsByIssueIDs(s.Ctx(), []string{"bd-cmt-wisp-cnt"}, domain.CommentOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal(2, out["bd-cmt-wisp-cnt"])

	// Without UseWispsTable the same issue ID would scan comments and find nothing.
	permOut, err := s.commentRepo().CountsByIssueIDs(s.Ctx(), []string{"bd-cmt-wisp-cnt"}, domain.CommentOpts{})
	s.Require().NoError(err)
	_, present := permOut["bd-cmt-wisp-cnt"]
	s.False(present)
}

func (s *testSuite) commentWispListRouting() {
	s.seedWispRow("bd-cmt-wisp-lst")
	base := time.Now().UTC().Truncate(time.Second)
	s.seedWispComment("bd-cmt-wisp-lst", "a", "first", base)
	s.seedWispComment("bd-cmt-wisp-lst", "a", "second", base.Add(time.Second))

	out, err := s.commentRepo().ListByIssueIDs(s.Ctx(), []string{"bd-cmt-wisp-lst"}, domain.CommentOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Require().Len(out["bd-cmt-wisp-lst"], 2)
	s.Equal("first", out["bd-cmt-wisp-lst"][0].Text)
	s.Equal("second", out["bd-cmt-wisp-lst"][1].Text)
}

func (s *testSuite) commentInsertEmptyID() {
	_, err := s.commentRepo().Insert(s.Ctx(), "", "alice", "hi", domain.CommentOpts{})
	s.Require().Error(err)
}

func (s *testSuite) commentInsertMissingIssue() {
	_, err := s.commentRepo().Insert(s.Ctx(), "bd-cmt-ins-ghost", "alice", "hi", domain.CommentOpts{})
	s.Require().Error(err)
	s.Contains(err.Error(), "not found")
}

func (s *testSuite) commentInsertMissingWisp() {
	s.seedIssueRow("bd-cmt-ins-perm-only")
	_, err := s.commentRepo().Insert(s.Ctx(), "bd-cmt-ins-perm-only", "alice", "hi", domain.CommentOpts{UseWispsTable: true})
	s.Require().Error(err)
	s.Contains(err.Error(), "not found")
}

func (s *testSuite) commentInsertPopulatesReturn() {
	s.seedIssueRow("bd-cmt-ins-ret")
	before := time.Now().UTC().Add(-time.Second)

	c, err := s.commentRepo().Insert(s.Ctx(), "bd-cmt-ins-ret", "alice", "hello world", domain.CommentOpts{})
	s.Require().NoError(err)
	s.Require().NotNil(c)
	s.NotEmpty(c.ID)
	s.Equal("bd-cmt-ins-ret", c.IssueID)
	s.Equal("alice", c.Author)
	s.Equal("hello world", c.Text)
	s.False(c.CreatedAt.Before(before), "CreatedAt should be stamped at insert time")
}

func (s *testSuite) commentInsertRoundTrip() {
	s.seedIssueRow("bd-cmt-ins-rt")

	inserted, err := s.commentRepo().Insert(s.Ctx(), "bd-cmt-ins-rt", "bob", "a note", domain.CommentOpts{})
	s.Require().NoError(err)

	out, err := s.commentRepo().ListByIssueIDs(s.Ctx(), []string{"bd-cmt-ins-rt"}, domain.CommentOpts{})
	s.Require().NoError(err)
	s.Require().Len(out["bd-cmt-ins-rt"], 1)
	got := out["bd-cmt-ins-rt"][0]
	s.Equal(inserted.ID, got.ID)
	s.Equal("bob", got.Author)
	s.Equal("a note", got.Text)
	s.WithinDuration(inserted.CreatedAt, got.CreatedAt, 2*time.Second)
}

func (s *testSuite) commentInsertWispRouting() {
	s.seedWispRow("bd-cmt-ins-wisp")

	inserted, err := s.commentRepo().Insert(s.Ctx(), "bd-cmt-ins-wisp", "carol", "wisp note", domain.CommentOpts{UseWispsTable: true})
	s.Require().NoError(err)

	wispOut, err := s.commentRepo().ListByIssueIDs(s.Ctx(), []string{"bd-cmt-ins-wisp"}, domain.CommentOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Require().Len(wispOut["bd-cmt-ins-wisp"], 1)
	s.Equal(inserted.ID, wispOut["bd-cmt-ins-wisp"][0].ID)
	s.Equal("wisp note", wispOut["bd-cmt-ins-wisp"][0].Text)

	permOut, err := s.commentRepo().ListByIssueIDs(s.Ctx(), []string{"bd-cmt-ins-wisp"}, domain.CommentOpts{})
	s.Require().NoError(err)
	s.Empty(permOut, "wisp comment should not land in the permanent comments table")
}

func (s *testSuite) commentWispIsolated() {
	s.seedIssueRow("bd-cmt-iso-perm")
	s.seedWispRow("bd-cmt-iso-wisp")
	now := time.Now().UTC()
	s.seedComment("bd-cmt-iso-perm", "a", "perm comment", now)
	s.seedWispComment("bd-cmt-iso-wisp", "a", "wisp comment", now)

	r := s.commentRepo()

	perm, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-cmt-iso-perm"}, domain.CommentOpts{})
	s.Require().NoError(err)
	s.Require().Len(perm["bd-cmt-iso-perm"], 1)
	s.Equal("perm comment", perm["bd-cmt-iso-perm"][0].Text)

	wisp, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-cmt-iso-wisp"}, domain.CommentOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Require().Len(wisp["bd-cmt-iso-wisp"], 1)
	s.Equal("wisp comment", wisp["bd-cmt-iso-wisp"][0].Text)

	// Cross-routed lookups return empty.
	crossPerm, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-cmt-iso-wisp"}, domain.CommentOpts{})
	s.Require().NoError(err)
	s.Empty(crossPerm)
	crossWisp, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-cmt-iso-perm"}, domain.CommentOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Empty(crossWisp)
}
