package db

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestDependencyUseCaseWithIssueMetadata() {
	s.Run("ListWithIssueMetadata", func() {
		s.Run("OutgoingDelegates", s.depUCListMetaOutgoing)
		s.Run("TypeFilterDelegates", s.depUCListMetaTypeFilter)
		s.Run("EmptyIDRejected", s.depUCListMetaEmptyID)
	})
	s.Run("IterWithIssueMetadata", func() {
		s.Run("StreamsSameRowsAsList", s.depUCIterMetaMatchesList)
		s.Run("EmptyIDRejected", s.depUCIterMetaEmptyID)
	})
	s.Run("CountByIssueID", func() {
		s.Run("BothSumsIncomingAndOutgoing", s.depUCCountByIDBoth)
		s.Run("EmptyIDRejected", s.depUCCountByIDEmptyID)
	})
	s.Run("Wisp", func() {
		s.Run("ListRoutesToWispOnSourceEdge", s.depUCWispListRouting)
		s.Run("CountRoutesToWispOnSourceEdge", s.depUCWispCountRouting)
	})
}

func (s *testSuite) depUCListMetaOutgoing() {
	s.seedIssueRow("bd-duc-meta-src")
	s.seedIssueRow("bd-duc-meta-a")
	s.seedIssueRow("bd-duc-meta-b")
	s.seedBlocksEdge("bd-duc-meta-src", "bd-duc-meta-a")
	s.seedBlocksEdge("bd-duc-meta-src", "bd-duc-meta-b")

	out, err := s.depUseCase().ListWithIssueMetadata(s.Ctx(), "bd-duc-meta-src",
		domain.DepListFilter{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	s.Require().Len(out, 2)
	for _, d := range out {
		s.Equal(types.DepBlocks, d.DependencyType)
		s.NotEmpty(d.ID)
	}
}

func (s *testSuite) depUCListMetaTypeFilter() {
	s.seedIssueRow("bd-duc-meta-tf-src")
	s.seedIssueRow("bd-duc-meta-tf-bl")
	s.seedIssueRow("bd-duc-meta-tf-rel")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-duc-meta-tf-src", "bd-duc-meta-tf-bl", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-duc-meta-tf-src", "bd-duc-meta-tf-rel", types.DepRelated), "tester", domain.DepInsertOpts{}))

	out, err := s.depUseCase().ListWithIssueMetadata(s.Ctx(), "bd-duc-meta-tf-src",
		domain.DepListFilter{
			Direction: domain.DepDirectionOut,
			Types:     []types.DependencyType{types.DepRelated},
		})
	s.Require().NoError(err)
	s.Require().Len(out, 1)
	s.Equal("bd-duc-meta-tf-rel", out[0].ID)
}

func (s *testSuite) depUCListMetaEmptyID() {
	_, err := s.depUseCase().ListWithIssueMetadata(s.Ctx(), "",
		domain.DepListFilter{Direction: domain.DepDirectionOut})
	s.Require().Error(err)
	s.Contains(err.Error(), "sourceID must not be empty")
}

func (s *testSuite) depUCIterMetaMatchesList() {
	s.seedIssueRow("bd-duc-iter-src")
	s.seedIssueRow("bd-duc-iter-a")
	s.seedBlocksEdge("bd-duc-iter-src", "bd-duc-iter-a")

	filter := domain.DepListFilter{Direction: domain.DepDirectionOut}
	list, err := s.depUseCase().ListWithIssueMetadata(s.Ctx(), "bd-duc-iter-src", filter)
	s.Require().NoError(err)

	it, err := s.depUseCase().IterWithIssueMetadata(s.Ctx(), "bd-duc-iter-src", filter)
	s.Require().NoError(err)
	defer it.Close() //nolint:errcheck

	var streamed []*types.IssueWithDependencyMetadata
	for it.Next(context.Background()) {
		streamed = append(streamed, it.Value())
	}
	s.Require().NoError(it.Err())
	s.Equal(len(list), len(streamed))
}

func (s *testSuite) depUCIterMetaEmptyID() {
	_, err := s.depUseCase().IterWithIssueMetadata(s.Ctx(), "",
		domain.DepListFilter{Direction: domain.DepDirectionOut})
	s.Require().Error(err)
}

func (s *testSuite) depUCCountByIDBoth() {
	s.seedIssueRow("bd-duc-cnt-x")
	s.seedIssueRow("bd-duc-cnt-up")
	s.seedIssueRow("bd-duc-cnt-down")
	s.seedBlocksEdge("bd-duc-cnt-x", "bd-duc-cnt-up")
	s.seedBlocksEdge("bd-duc-cnt-down", "bd-duc-cnt-x")

	n, err := s.depUseCase().CountByIssueID(s.Ctx(), "bd-duc-cnt-x",
		domain.DepListFilter{Direction: domain.DepDirectionBoth})
	s.Require().NoError(err)
	s.Equal(int64(2), n)
}

func (s *testSuite) depUCCountByIDEmptyID() {
	_, err := s.depUseCase().CountByIssueID(s.Ctx(), "",
		domain.DepListFilter{Direction: domain.DepDirectionBoth})
	s.Require().Error(err)
}

func (s *testSuite) depUCWispListRouting() {
	s.seedIssueRow("bd-duc-wisp-tgt")
	s.seedWispRow("bd-duc-wisp-src")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-duc-wisp-src", "bd-duc-wisp-tgt", types.DepBlocks), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))

	out, err := s.depUseCase().ListWispWithIssueMetadata(s.Ctx(), "bd-duc-wisp-src",
		domain.DepListFilter{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	s.Require().Len(out, 1)
	s.Equal("bd-duc-wisp-tgt", out[0].ID)
}

func (s *testSuite) depUCWispCountRouting() {
	s.seedIssueRow("bd-duc-wisp-cnt-tgt")
	s.seedWispRow("bd-duc-wisp-cnt-src")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-duc-wisp-cnt-src", "bd-duc-wisp-cnt-tgt", types.DepBlocks), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))

	n, err := s.depUseCase().CountByWispID(s.Ctx(), "bd-duc-wisp-cnt-src",
		domain.DepListFilter{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	s.Equal(int64(1), n)
}
