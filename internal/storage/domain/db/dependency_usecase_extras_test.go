package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestDependencyUseCase_Extras() {
	s.Run("GetDependencyTree", func() {
		s.Run("EmptyRootIDReturnsError", s.ducTreeEmptyRoot)
		s.Run("DelegatesToRepo", s.ducTreeDelegates)
	})
	s.Run("GetIssueDependencyRecords", func() {
		s.Run("EmptyReturnsEmptyMapNoRepoCall", s.ducGetIssueRecsEmpty)
		s.Run("DelegatesToRepo", s.ducGetIssueRecsDelegates)
	})
	s.Run("GetWispDependencyRecords", func() {
		s.Run("EmptyReturnsEmptyMap", s.ducGetWispRecsEmpty)
		s.Run("DelegatesToRepoWispTable", s.ducGetWispRecsDelegates)
	})
	s.Run("AddDependencies", func() {
		s.Run("EmptySliceReturnsEmptyResult", s.ducAddBulkEmpty)
		s.Run("NilDepReturnsError", s.ducAddBulkNilDep)
		s.Run("EmptyIDsReturnError", s.ducAddBulkEmptyIDs)
		s.Run("InsertsEdgesAndReturnsAdded", s.ducAddBulkInserts)
		s.Run("PerEdgeCycleCheckBlocksCycleCreation", s.ducAddBulkPerEdgeCycle)
		s.Run("SkipPerEdgeStillRunsFinalCheck", s.ducAddBulkFinalCycleCheck)
		s.Run("SkipPerEdgeAcceptsAcyclicBulk", s.ducAddBulkSkipPerEdgeAcyclic)
		s.Run("NonBlockingEdgesSkipCycleChecks", s.ducAddBulkNonBlockingNoCheck)
	})
	s.Run("AddWispDependencies", func() {
		s.Run("RoutesToWispDepsTable", s.ducAddBulkWispRoutes)
	})
}

// ---- GetDependencyTree ----

func (s *testSuite) ducTreeEmptyRoot() {
	_, err := s.depUseCase().GetDependencyTree(s.Ctx(), "", domain.DepTreeOpts{
		MaxDepth:  5,
		Direction: domain.DepDirectionOut,
	})
	s.Require().Error(err)
}

func (s *testSuite) ducTreeDelegates() {
	// Verify the UC passes opts through and returns repo output: build a chain
	// a -> b and assert both nodes come back at the correct depths.
	s.seedIssueRow("bd-duc-tree-a")
	s.seedIssueRow("bd-duc-tree-b")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		newDep("bd-duc-tree-a", "bd-duc-tree-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := s.depUseCase().GetDependencyTree(s.Ctx(), "bd-duc-tree-a", domain.DepTreeOpts{
		MaxDepth:  5,
		Direction: domain.DepDirectionOut,
	})
	s.Require().NoError(err)
	byID := map[string]*types.TreeNode{}
	for _, n := range out {
		byID[n.ID] = n
	}
	s.Require().Contains(byID, "bd-duc-tree-a")
	s.Require().Contains(byID, "bd-duc-tree-b")
	s.Equal(0, byID["bd-duc-tree-a"].Depth)
	s.Equal(1, byID["bd-duc-tree-b"].Depth)
}

// ---- GetIssueDependencyRecords ----

func (s *testSuite) ducGetIssueRecsEmpty() {
	out, err := s.depUseCase().GetIssueDependencyRecords(s.Ctx(), nil)
	s.Require().NoError(err)
	s.NotNil(out)
	s.Empty(out)
}

func (s *testSuite) ducGetIssueRecsDelegates() {
	s.seedIssueRow("bd-duc-girecs-src")
	s.seedIssueRow("bd-duc-girecs-tgt")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		newDep("bd-duc-girecs-src", "bd-duc-girecs-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := s.depUseCase().GetIssueDependencyRecords(s.Ctx(), []string{"bd-duc-girecs-src"})
	s.Require().NoError(err)
	s.Require().Len(out["bd-duc-girecs-src"], 1)
	s.Equal("bd-duc-girecs-tgt", out["bd-duc-girecs-src"][0].DependsOnID)
}

// ---- GetWispDependencyRecords ----

func (s *testSuite) ducGetWispRecsEmpty() {
	out, err := s.depUseCase().GetWispDependencyRecords(s.Ctx(), nil)
	s.Require().NoError(err)
	s.NotNil(out)
	s.Empty(out)
}

func (s *testSuite) ducGetWispRecsDelegates() {
	s.seedWispRow("bd-duc-gwrecs-src")
	s.seedIssueRow("bd-duc-gwrecs-tgt")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		newDep("bd-duc-gwrecs-src", "bd-duc-gwrecs-tgt", types.DepBlocks), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))

	out, err := s.depUseCase().GetWispDependencyRecords(s.Ctx(), []string{"bd-duc-gwrecs-src"})
	s.Require().NoError(err)
	s.Require().Len(out["bd-duc-gwrecs-src"], 1)
	s.Equal("bd-duc-gwrecs-tgt", out["bd-duc-gwrecs-src"][0].DependsOnID)
}

// ---- AddDependencies ----

func (s *testSuite) ducAddBulkEmpty() {
	res, err := s.depUseCase().AddDependencies(s.Ctx(), nil, "tester", domain.BulkAddDepsOpts{})
	s.Require().NoError(err)
	s.NotNil(res.Added)
	s.Empty(res.Added)
}

func (s *testSuite) ducAddBulkNilDep() {
	deps := []*types.Dependency{nil}
	_, err := s.depUseCase().AddDependencies(s.Ctx(), deps, "tester", domain.BulkAddDepsOpts{})
	s.Require().Error(err)
}

func (s *testSuite) ducAddBulkEmptyIDs() {
	uc := s.depUseCase()
	_, err := uc.AddDependencies(s.Ctx(),
		[]*types.Dependency{newDep("", "bd-x", types.DepBlocks)}, "tester", domain.BulkAddDepsOpts{})
	s.Require().Error(err)
	_, err = uc.AddDependencies(s.Ctx(),
		[]*types.Dependency{newDep("bd-x", "", types.DepBlocks)}, "tester", domain.BulkAddDepsOpts{})
	s.Require().Error(err)
}

func (s *testSuite) ducAddBulkInserts() {
	s.seedIssueRow("bd-duc-bulk-a")
	s.seedIssueRow("bd-duc-bulk-b")
	s.seedIssueRow("bd-duc-bulk-c")

	deps := []*types.Dependency{
		newDep("bd-duc-bulk-a", "bd-duc-bulk-b", types.DepBlocks),
		newDep("bd-duc-bulk-a", "bd-duc-bulk-c", types.DepBlocks),
	}
	res, err := s.depUseCase().AddDependencies(s.Ctx(), deps, "tester", domain.BulkAddDepsOpts{})
	s.Require().NoError(err)
	s.Require().Len(res.Added, 2)

	// Verify both rows landed.
	out, err := s.depUseCase().GetIssueDependencyRecords(s.Ctx(), []string{"bd-duc-bulk-a"})
	s.Require().NoError(err)
	s.Require().Len(out["bd-duc-bulk-a"], 2)
}

func (s *testSuite) ducAddBulkPerEdgeCycle() {
	// Existing a -> b. Trying to add b -> a must fail the per-edge cycle check
	// and NOT insert the new edge.
	s.seedIssueRow("bd-duc-pec-a")
	s.seedIssueRow("bd-duc-pec-b")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		newDep("bd-duc-pec-a", "bd-duc-pec-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	_, err := s.depUseCase().AddDependencies(s.Ctx(),
		[]*types.Dependency{newDep("bd-duc-pec-b", "bd-duc-pec-a", types.DepBlocks)},
		"tester", domain.BulkAddDepsOpts{})
	s.Require().Error(err, "per-edge cycle check must block the new edge")

	var count int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
		"bd-duc-pec-b", "bd-duc-pec-a").Scan(&count))
	s.Equal(0, count, "cycle-rejected edge must not have been inserted")
}

func (s *testSuite) ducAddBulkFinalCycleCheck() {
	// With SkipPerEdgeCycleCheck, the per-edge guard is bypassed but the final
	// whole-graph CycleThroughEdges call still fails the operation. The CLI
	// runs this inside a UOW so rollback happens at the transaction boundary;
	// here we just assert that the error is returned. (Inserted rows from the
	// pre-failure loop iterations are NOT rolled back at the UC level — the
	// UC's contract is "return error so the surrounding UOW rolls back".)
	s.seedIssueRow("bd-duc-fcc-a")
	s.seedIssueRow("bd-duc-fcc-b")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		newDep("bd-duc-fcc-a", "bd-duc-fcc-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	_, err := s.depUseCase().AddDependencies(s.Ctx(),
		[]*types.Dependency{newDep("bd-duc-fcc-b", "bd-duc-fcc-a", types.DepBlocks)},
		"tester", domain.BulkAddDepsOpts{SkipPerEdgeCycleCheck: true})
	s.Require().Error(err, "final whole-graph cycle check must fail the bulk add")
	s.Contains(err.Error(), "cycle")
}

func (s *testSuite) ducAddBulkSkipPerEdgeAcyclic() {
	// SkipPerEdgeCycleCheck + acyclic edges → success.
	s.seedIssueRow("bd-duc-spa-a")
	s.seedIssueRow("bd-duc-spa-b")
	s.seedIssueRow("bd-duc-spa-c")

	deps := []*types.Dependency{
		newDep("bd-duc-spa-a", "bd-duc-spa-b", types.DepBlocks),
		newDep("bd-duc-spa-b", "bd-duc-spa-c", types.DepBlocks),
	}
	res, err := s.depUseCase().AddDependencies(s.Ctx(), deps, "tester",
		domain.BulkAddDepsOpts{SkipPerEdgeCycleCheck: true})
	s.Require().NoError(err)
	s.Require().Len(res.Added, 2)
}

func (s *testSuite) ducAddBulkNonBlockingNoCheck() {
	// Non-blocking types (related) skip the cycle check entirely — adding a
	// "related" edge between two issues that already cycle on blocking must
	// succeed without complaint.
	s.seedIssueRow("bd-duc-nbnc-a")
	s.seedIssueRow("bd-duc-nbnc-b")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		newDep("bd-duc-nbnc-a", "bd-duc-nbnc-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	// related edge b -> a: cycle check is per-edge for blocking types only, so
	// this should always be accepted.
	res, err := s.depUseCase().AddDependencies(s.Ctx(),
		[]*types.Dependency{newDep("bd-duc-nbnc-b", "bd-duc-nbnc-a", types.DepRelated)},
		"tester", domain.BulkAddDepsOpts{})
	s.Require().NoError(err)
	s.Require().Len(res.Added, 1)
}

// ---- AddWispDependencies ----

func (s *testSuite) ducAddBulkWispRoutes() {
	s.seedWispRow("bd-duc-bw-src")
	s.seedIssueRow("bd-duc-bw-tgt")

	deps := []*types.Dependency{
		newDep("bd-duc-bw-src", "bd-duc-bw-tgt", types.DepBlocks),
	}
	res, err := s.depUseCase().AddWispDependencies(s.Ctx(), deps, "tester", domain.BulkAddDepsOpts{})
	s.Require().NoError(err)
	s.Require().Len(res.Added, 1)

	var wispCount, permCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?", "bd-duc-bw-src").Scan(&wispCount))
	s.Equal(1, wispCount)
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", "bd-duc-bw-src").Scan(&permCount))
	s.Equal(0, permCount, "wisp-routed bulk add must not touch the issues dep table")
}
