package db

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestDependencySQLRepositoryWithIssueMetadata() {
	s.Run("ListWithIssueMetadata", func() {
		s.Run("OutgoingHydratesTargetFields", s.depMetaListOutgoing)
		s.Run("IncomingHydratesSourceFields", s.depMetaListIncoming)
		s.Run("BothConcatenatesBoth", s.depMetaListBoth)
		s.Run("TypeFilterApplied", s.depMetaListTypeFilter)
		s.Run("EmptyWhenNoEdges", s.depMetaListEmpty)
		s.Run("CrossesPermAndWispEdgeTables", s.depMetaListCrossTables)
	})
	s.Run("IterWithIssueMetadata", func() {
		s.Run("StreamsSameRowsAsList", s.depMetaIterMatchesList)
		s.Run("EmptyIteratorIsValid", s.depMetaIterEmpty)
	})
	s.Run("CountByID", func() {
		s.Run("OutgoingSumsBothEdgeTables", s.depCountByIDOutgoing)
		s.Run("IncomingSumsBothEdgeTables", s.depCountByIDIncoming)
		s.Run("BothSumsAllEdges", s.depCountByIDBoth)
		s.Run("TypeFilterApplied", s.depCountByIDTypeFilter)
		s.Run("ReturnsZeroOnNoEdges", s.depCountByIDZero)
	})
}

func (s *testSuite) seedBlocksEdge(issueID, dependsOnID string) {
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep(issueID, dependsOnID, types.DepBlocks), "tester", domain.DepInsertOpts{}))
}

func (s *testSuite) depMetaListOutgoing() {
	s.seedIssueRow("bd-meta-out-src")
	s.seedIssueRow("bd-meta-out-t1")
	s.seedIssueRow("bd-meta-out-t2")
	_, err := s.Runner().ExecContext(s.Ctx(),
		"UPDATE issues SET title = ?, priority = ?, status = ? WHERE id = ?",
		"target one", 1, string(types.StatusInProgress), "bd-meta-out-t1")
	s.Require().NoError(err)
	s.seedBlocksEdge("bd-meta-out-src", "bd-meta-out-t1")
	s.seedBlocksEdge("bd-meta-out-src", "bd-meta-out-t2")

	out, err := s.depRepo().ListWithIssueMetadata(s.Ctx(), "bd-meta-out-src",
		domain.DepListOpts{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	s.Require().Len(out, 2)
	got := make(map[string]*types.IssueWithDependencyMetadata, len(out))
	for _, d := range out {
		got[d.ID] = d
	}
	s.Require().NotNil(got["bd-meta-out-t1"])
	s.Equal(types.DepBlocks, got["bd-meta-out-t1"].DependencyType)
	s.NotEmpty(got["bd-meta-out-t1"].Title, "joined issue row title must be hydrated")
}

func (s *testSuite) depMetaListIncoming() {
	s.seedIssueRow("bd-meta-in-tgt")
	s.seedIssueRow("bd-meta-in-s1")
	s.seedIssueRow("bd-meta-in-s2")
	s.seedBlocksEdge("bd-meta-in-s1", "bd-meta-in-tgt")
	s.seedBlocksEdge("bd-meta-in-s2", "bd-meta-in-tgt")

	out, err := s.depRepo().ListWithIssueMetadata(s.Ctx(), "bd-meta-in-tgt",
		domain.DepListOpts{Direction: domain.DepDirectionIn})
	s.Require().NoError(err)
	s.Require().Len(out, 2)
	for _, d := range out {
		s.Equal(types.DepBlocks, d.DependencyType)
		s.NotEmpty(d.ID)
	}
}

func (s *testSuite) depMetaListBoth() {
	s.seedIssueRow("bd-meta-both-x")
	s.seedIssueRow("bd-meta-both-up")
	s.seedIssueRow("bd-meta-both-down")
	s.seedBlocksEdge("bd-meta-both-x", "bd-meta-both-up")
	s.seedBlocksEdge("bd-meta-both-down", "bd-meta-both-x")

	out, err := s.depRepo().ListWithIssueMetadata(s.Ctx(), "bd-meta-both-x",
		domain.DepListOpts{Direction: domain.DepDirectionBoth})
	s.Require().NoError(err)
	s.Require().Len(out, 2, "Both returns outgoing + incoming concatenated")
	seen := map[string]bool{}
	for _, d := range out {
		seen[d.ID] = true
	}
	s.True(seen["bd-meta-both-up"])
	s.True(seen["bd-meta-both-down"])
}

func (s *testSuite) depMetaListTypeFilter() {
	s.seedIssueRow("bd-meta-tf-src")
	s.seedIssueRow("bd-meta-tf-bl")
	s.seedIssueRow("bd-meta-tf-rel")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-meta-tf-src", "bd-meta-tf-bl", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-meta-tf-src", "bd-meta-tf-rel", types.DepRelated), "tester", domain.DepInsertOpts{}))

	out, err := s.depRepo().ListWithIssueMetadata(s.Ctx(), "bd-meta-tf-src",
		domain.DepListOpts{
			Direction: domain.DepDirectionOut,
			Types:     []types.DependencyType{types.DepBlocks},
		})
	s.Require().NoError(err)
	s.Require().Len(out, 1)
	s.Equal("bd-meta-tf-bl", out[0].ID)
	s.Equal(types.DepBlocks, out[0].DependencyType)
}

func (s *testSuite) depMetaListEmpty() {
	s.seedIssueRow("bd-meta-empty")
	out, err := s.depRepo().ListWithIssueMetadata(s.Ctx(), "bd-meta-empty",
		domain.DepListOpts{Direction: domain.DepDirectionBoth})
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) depMetaListCrossTables() {
	s.seedIssueRow("bd-meta-x-tgt")
	s.seedIssueRow("bd-meta-x-perm-src")
	s.seedWispRow("bd-meta-x-wisp-src")

	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-meta-x-perm-src", "bd-meta-x-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-meta-x-wisp-src", "bd-meta-x-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{UseWispsTable: true}))

	out, err := s.depRepo().ListWithIssueMetadata(s.Ctx(), "bd-meta-x-tgt",
		domain.DepListOpts{Direction: domain.DepDirectionIn})
	s.Require().NoError(err)
	s.Require().Len(out, 2, "Incoming must union dependencies + wisp_dependencies")

	n, err := s.depRepo().CountByID(s.Ctx(), "bd-meta-x-tgt",
		domain.DepListOpts{Direction: domain.DepDirectionIn})
	s.Require().NoError(err)
	s.Equal(int64(2), n, "CountByID Incoming must sum both edge tables")
}

func (s *testSuite) depMetaIterMatchesList() {
	s.seedIssueRow("bd-meta-iter-src")
	s.seedIssueRow("bd-meta-iter-a")
	s.seedIssueRow("bd-meta-iter-b")
	s.seedBlocksEdge("bd-meta-iter-src", "bd-meta-iter-a")
	s.seedBlocksEdge("bd-meta-iter-src", "bd-meta-iter-b")

	opts := domain.DepListOpts{Direction: domain.DepDirectionOut}
	list, err := s.depRepo().ListWithIssueMetadata(s.Ctx(), "bd-meta-iter-src", opts)
	s.Require().NoError(err)

	it, err := s.depRepo().IterWithIssueMetadata(s.Ctx(), "bd-meta-iter-src", opts)
	s.Require().NoError(err)
	defer it.Close() //nolint:errcheck

	var streamed []*types.IssueWithDependencyMetadata
	for it.Next(context.Background()) {
		streamed = append(streamed, it.Value())
	}
	s.Require().NoError(it.Err())
	s.Equal(len(list), len(streamed))
}

func (s *testSuite) depMetaIterEmpty() {
	s.seedIssueRow("bd-meta-iter-empty")
	it, err := s.depRepo().IterWithIssueMetadata(s.Ctx(), "bd-meta-iter-empty",
		domain.DepListOpts{Direction: domain.DepDirectionBoth})
	s.Require().NoError(err)
	defer it.Close() //nolint:errcheck
	s.False(it.Next(context.Background()))
	s.NoError(it.Err())
}

func (s *testSuite) depCountByIDOutgoing() {
	s.seedIssueRow("bd-cbi-out-src")
	s.seedIssueRow("bd-cbi-out-a")
	s.seedIssueRow("bd-cbi-out-b")
	s.seedBlocksEdge("bd-cbi-out-src", "bd-cbi-out-a")
	s.seedBlocksEdge("bd-cbi-out-src", "bd-cbi-out-b")

	n, err := s.depRepo().CountByID(s.Ctx(), "bd-cbi-out-src",
		domain.DepListOpts{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	s.Equal(int64(2), n)
}

func (s *testSuite) depCountByIDIncoming() {
	s.seedIssueRow("bd-cbi-in-tgt")
	s.seedIssueRow("bd-cbi-in-s1")
	s.seedIssueRow("bd-cbi-in-s2")
	s.seedIssueRow("bd-cbi-in-s3")
	s.seedBlocksEdge("bd-cbi-in-s1", "bd-cbi-in-tgt")
	s.seedBlocksEdge("bd-cbi-in-s2", "bd-cbi-in-tgt")
	s.seedBlocksEdge("bd-cbi-in-s3", "bd-cbi-in-tgt")

	n, err := s.depRepo().CountByID(s.Ctx(), "bd-cbi-in-tgt",
		domain.DepListOpts{Direction: domain.DepDirectionIn})
	s.Require().NoError(err)
	s.Equal(int64(3), n)
}

func (s *testSuite) depCountByIDBoth() {
	s.seedIssueRow("bd-cbi-both-x")
	s.seedIssueRow("bd-cbi-both-up")
	s.seedIssueRow("bd-cbi-both-down-a")
	s.seedIssueRow("bd-cbi-both-down-b")
	s.seedBlocksEdge("bd-cbi-both-x", "bd-cbi-both-up")
	s.seedBlocksEdge("bd-cbi-both-down-a", "bd-cbi-both-x")
	s.seedBlocksEdge("bd-cbi-both-down-b", "bd-cbi-both-x")

	n, err := s.depRepo().CountByID(s.Ctx(), "bd-cbi-both-x",
		domain.DepListOpts{Direction: domain.DepDirectionBoth})
	s.Require().NoError(err)
	s.Equal(int64(3), n, "Both must sum outgoing (1) and incoming (2)")
}

func (s *testSuite) depCountByIDTypeFilter() {
	s.seedIssueRow("bd-cbi-tf-src")
	s.seedIssueRow("bd-cbi-tf-bl")
	s.seedIssueRow("bd-cbi-tf-rel")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-cbi-tf-src", "bd-cbi-tf-bl", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-cbi-tf-src", "bd-cbi-tf-rel", types.DepRelated), "tester", domain.DepInsertOpts{}))

	n, err := s.depRepo().CountByID(s.Ctx(), "bd-cbi-tf-src",
		domain.DepListOpts{
			Direction: domain.DepDirectionOut,
			Types:     []types.DependencyType{types.DepBlocks},
		})
	s.Require().NoError(err)
	s.Equal(int64(1), n)
}

func (s *testSuite) depCountByIDZero() {
	s.seedIssueRow("bd-cbi-zero")
	n, err := s.depRepo().CountByID(s.Ctx(), "bd-cbi-zero",
		domain.DepListOpts{Direction: domain.DepDirectionBoth})
	s.Require().NoError(err)
	s.Equal(int64(0), n)
}
