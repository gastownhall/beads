package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestDependencySQLRepository_Extras() {
	s.Run("GetTree", func() {
		s.Run("EmptyRootIDReturnsError", s.depTreeEmptyRoot)
		s.Run("BothDirectionRejected", s.depTreeBothRejected)
		s.Run("SingleNodeNoDeps", s.depTreeSingleNode)
		s.Run("DirectionOutTraversesDependencies", s.depTreeDirectionOut)
		s.Run("DirectionInTraversesDependents", s.depTreeDirectionIn)
		s.Run("MaxDepthRespected", s.depTreeMaxDepth)
		s.Run("ZeroMaxDepthFallsBackToDefault", s.depTreeMaxDepthDefault)
		s.Run("CycleVisitedOnce", s.depTreeCycleVisitedOnce)
	})
	s.Run("CycleThroughEdges", func() {
		s.Run("EmptyEdgesReturnsEmpty", s.depCTEEmpty)
		s.Run("NoCycleReturnsEmpty", s.depCTENoCycle)
		s.Run("CycleThroughNewEdgeDetected", s.depCTEDetected)
		s.Run("NonBlockingTypesIgnored", s.depCTENonBlockingIgnored)
		s.Run("CycleAcrossWispTable", s.depCTEAcrossWisp)
		s.Run("UnrelatedExistingCycleIgnored", s.depCTEUnrelatedCycleIgnored)
	})
	s.Run("GetDependencyRecordsForIssues", func() {
		s.Run("EmptyReturnsEmptyMap", s.depRecsEmpty)
		s.Run("ReturnsRecordsForIssues", s.depRecsBasic)
		s.Run("WispIDsRoutedToWispTable", s.depRecsWispRouted)
		s.Run("MissingIDAbsentFromMap", s.depRecsMissingID)
	})
	s.Run("GetWispDependencyRecordsForIDs", func() {
		s.Run("EmptyReturnsEmptyMap", s.depWispRecsEmpty)
		s.Run("ReturnsOnlyFromWispTable", s.depWispRecsForcedTable)
		s.Run("PermSourceWithSameIDIgnored", s.depWispRecsIgnoresPerm)
	})
}

// ---- GetTree ----

func (s *testSuite) depTreeEmptyRoot() {
	_, err := s.depRepo().GetTree(s.Ctx(), "", domain.DepTreeOpts{MaxDepth: 5})
	s.Require().Error(err)
}

func (s *testSuite) depTreeBothRejected() {
	s.seedIssueRow("bd-tree-both")
	_, err := s.depRepo().GetTree(s.Ctx(), "bd-tree-both", domain.DepTreeOpts{
		MaxDepth:  5,
		Direction: domain.DepDirectionBoth,
	})
	s.Require().Error(err, "Both direction must be rejected; callers merge two calls")
}

func (s *testSuite) depTreeSingleNode() {
	s.seedIssueRow("bd-tree-solo")
	out, err := s.depRepo().GetTree(s.Ctx(), "bd-tree-solo", domain.DepTreeOpts{
		MaxDepth:  5,
		Direction: domain.DepDirectionOut,
	})
	s.Require().NoError(err)
	s.Require().Len(out, 1, "no edges → just the root")
	s.Equal("bd-tree-solo", out[0].ID)
	s.Equal(0, out[0].Depth)
	s.Empty(out[0].ParentID)
}

func (s *testSuite) depTreeDirectionOut() {
	// Chain: a -> b -> c (a depends on b depends on c)
	s.seedIssueRow("bd-tree-out-a")
	s.seedIssueRow("bd-tree-out-b")
	s.seedIssueRow("bd-tree-out-c")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-tree-out-a", "bd-tree-out-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-tree-out-b", "bd-tree-out-c", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.GetTree(s.Ctx(), "bd-tree-out-a", domain.DepTreeOpts{
		MaxDepth:  5,
		Direction: domain.DepDirectionOut,
	})
	s.Require().NoError(err)

	byID := map[string]*types.TreeNode{}
	for _, n := range out {
		byID[n.ID] = n
	}
	s.Require().Contains(byID, "bd-tree-out-a")
	s.Require().Contains(byID, "bd-tree-out-b")
	s.Require().Contains(byID, "bd-tree-out-c")
	s.Equal(0, byID["bd-tree-out-a"].Depth)
	s.Equal(1, byID["bd-tree-out-b"].Depth)
	s.Equal(2, byID["bd-tree-out-c"].Depth)
	s.Equal("bd-tree-out-a", byID["bd-tree-out-b"].ParentID)
	s.Equal("bd-tree-out-b", byID["bd-tree-out-c"].ParentID)
}

func (s *testSuite) depTreeDirectionIn() {
	// a -> root, b -> root: root's dependents are a and b.
	s.seedIssueRow("bd-tree-in-root")
	s.seedIssueRow("bd-tree-in-a")
	s.seedIssueRow("bd-tree-in-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-tree-in-a", "bd-tree-in-root", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-tree-in-b", "bd-tree-in-root", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.GetTree(s.Ctx(), "bd-tree-in-root", domain.DepTreeOpts{
		MaxDepth:  5,
		Direction: domain.DepDirectionIn,
	})
	s.Require().NoError(err)
	ids := map[string]bool{}
	for _, n := range out {
		ids[n.ID] = true
	}
	s.Contains(ids, "bd-tree-in-root")
	s.Contains(ids, "bd-tree-in-a")
	s.Contains(ids, "bd-tree-in-b")
}

func (s *testSuite) depTreeMaxDepth() {
	// a -> b -> c. MaxDepth=1 must stop at depth 0; c (depth 2) absent.
	// (Per buildDependencyTreeInTx, depth >= maxDepth halts traversal.)
	s.seedIssueRow("bd-tree-md-a")
	s.seedIssueRow("bd-tree-md-b")
	s.seedIssueRow("bd-tree-md-c")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-tree-md-a", "bd-tree-md-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-tree-md-b", "bd-tree-md-c", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.GetTree(s.Ctx(), "bd-tree-md-a", domain.DepTreeOpts{
		MaxDepth:  1,
		Direction: domain.DepDirectionOut,
	})
	s.Require().NoError(err)
	ids := map[string]bool{}
	for _, n := range out {
		ids[n.ID] = true
	}
	s.True(ids["bd-tree-md-a"])
	s.False(ids["bd-tree-md-c"], "depth-2 node must be pruned by MaxDepth=1")
}

func (s *testSuite) depTreeMaxDepthDefault() {
	// MaxDepth=0 should fall back to the repo's default and traverse normally.
	s.seedIssueRow("bd-tree-mdd-a")
	s.seedIssueRow("bd-tree-mdd-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-tree-mdd-a", "bd-tree-mdd-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	out, err := r.GetTree(s.Ctx(), "bd-tree-mdd-a", domain.DepTreeOpts{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	ids := map[string]bool{}
	for _, n := range out {
		ids[n.ID] = true
	}
	s.True(ids["bd-tree-mdd-b"], "MaxDepth=0 must default, not prune everything below the root")
}

func (s *testSuite) depTreeCycleVisitedOnce() {
	// a -> b, b -> a. Traversal must terminate; both nodes appear at most twice
	// (root + revisit hint), and the traversal must not infinite-loop.
	s.seedIssueRow("bd-tree-cyc-a")
	s.seedIssueRow("bd-tree-cyc-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-tree-cyc-a", "bd-tree-cyc-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-tree-cyc-b", "bd-tree-cyc-a", types.DepBlocks), "tester", domain.DepInsertOpts{CycleValidated: true}))

	out, err := r.GetTree(s.Ctx(), "bd-tree-cyc-a", domain.DepTreeOpts{
		MaxDepth:  10,
		Direction: domain.DepDirectionOut,
	})
	s.Require().NoError(err)
	s.NotEmpty(out)
}

// ---- CycleThroughEdges ----

func (s *testSuite) depCTEEmpty() {
	out, err := s.depRepo().CycleThroughEdges(s.Ctx(), nil)
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) depCTENoCycle() {
	s.seedIssueRow("bd-cte-nc-a")
	s.seedIssueRow("bd-cte-nc-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cte-nc-a", "bd-cte-nc-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.CycleThroughEdges(s.Ctx(), [][2]string{{"bd-cte-nc-a", "bd-cte-nc-b"}})
	s.Require().NoError(err)
	s.Empty(out, "non-cyclic edge must return empty cycle path")
}

func (s *testSuite) depCTEDetected() {
	// Setup: a -> b already in graph. Caller is about to add b -> a.
	// The graph already contains the new edge (callers insert before checking),
	// so cycle search starting at a (the source) finds it.
	s.seedIssueRow("bd-cte-det-a")
	s.seedIssueRow("bd-cte-det-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cte-det-a", "bd-cte-det-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cte-det-b", "bd-cte-det-a", types.DepBlocks), "tester", domain.DepInsertOpts{CycleValidated: true}))

	out, err := r.CycleThroughEdges(s.Ctx(), [][2]string{{"bd-cte-det-b", "bd-cte-det-a"}})
	s.Require().NoError(err)
	s.NotEmpty(out, "cycle through the new edge must be reported")
	s.Contains(out, "bd-cte-det-a")
	s.Contains(out, "bd-cte-det-b")
}

func (s *testSuite) depCTENonBlockingIgnored() {
	// related-only edges must not form a cycle even when the topology would
	// close one under blocking semantics.
	s.seedIssueRow("bd-cte-nb-a")
	s.seedIssueRow("bd-cte-nb-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cte-nb-a", "bd-cte-nb-b", types.DepRelated), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cte-nb-b", "bd-cte-nb-a", types.DepRelated), "tester", domain.DepInsertOpts{}))

	out, err := r.CycleThroughEdges(s.Ctx(), [][2]string{{"bd-cte-nb-b", "bd-cte-nb-a"}})
	s.Require().NoError(err)
	s.Empty(out, "related-only edges form no blocking cycle")
}

func (s *testSuite) depCTEAcrossWisp() {
	// Wisp source w blocks issue a (in wisp_dependencies); issue a blocks wisp
	// w via raw insert into wisp_dependencies as source=w-source-of-truth?
	// To keep this simple, exercise the table union: a -> b in perm and
	// b -> a in wisp_dependencies. The graph union must close the cycle.
	s.seedIssueRow("bd-cte-w-a")
	s.seedWispRow("bd-cte-w-b")
	r := s.depRepo()
	// Perm edge: bd-cte-w-a -> bd-cte-w-b. Target is a wisp, so use raw SQL
	// (Insert writes depends_on_issue_id only).
	_, err := s.Runner().ExecContext(s.Ctx(), `
		INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by, metadata)
		VALUES (UUID(), ?, ?, 'blocks', NOW(), 'tester', '{}')
	`, "bd-cte-w-a", "bd-cte-w-b")
	s.Require().NoError(err)
	// Wisp edge: bd-cte-w-b (wisp) -> bd-cte-w-a (perm) in wisp_dependencies.
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-cte-w-b", "bd-cte-w-a", types.DepBlocks), "tester",
		domain.DepInsertOpts{UseWispsTable: true, CycleValidated: true}))

	out, err := r.CycleThroughEdges(s.Ctx(), [][2]string{{"bd-cte-w-b", "bd-cte-w-a"}})
	s.Require().NoError(err)
	s.NotEmpty(out, "cycle spanning dependencies + wisp_dependencies must be detected")
}

func (s *testSuite) depCTEUnrelatedCycleIgnored() {
	// Pre-existing cycle x -> y -> x exists. A separate, acyclic new edge
	// p -> q must not be flagged just because *some* cycle exists (bd-578h9.9).
	s.seedIssueRow("bd-cte-uc-x")
	s.seedIssueRow("bd-cte-uc-y")
	s.seedIssueRow("bd-cte-uc-p")
	s.seedIssueRow("bd-cte-uc-q")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cte-uc-x", "bd-cte-uc-y", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	// Insert y -> x via raw SQL to bypass cycle check; we want the graph in a
	// cyclic state without going through the use case validator.
	_, err := s.Runner().ExecContext(s.Ctx(), `
		INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata)
		VALUES (UUID(), ?, ?, 'blocks', NOW(), 'tester', '{}')
	`, "bd-cte-uc-y", "bd-cte-uc-x")
	s.Require().NoError(err)
	// p -> q: acyclic new edge.
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cte-uc-p", "bd-cte-uc-q", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.CycleThroughEdges(s.Ctx(), [][2]string{{"bd-cte-uc-p", "bd-cte-uc-q"}})
	s.Require().NoError(err)
	s.Empty(out, "pre-existing cycle not touching the new edge must not block it")
}

// ---- GetDependencyRecordsForIssues ----

func (s *testSuite) depRecsEmpty() {
	out, err := s.depRepo().GetDependencyRecordsForIssues(s.Ctx(), nil)
	s.Require().NoError(err)
	s.NotNil(out)
	s.Empty(out)
}

func (s *testSuite) depRecsBasic() {
	s.seedIssueRow("bd-recs-src")
	s.seedIssueRow("bd-recs-a")
	s.seedIssueRow("bd-recs-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-recs-src", "bd-recs-a", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-recs-src", "bd-recs-b", types.DepRelated), "tester", domain.DepInsertOpts{}))

	out, err := r.GetDependencyRecordsForIssues(s.Ctx(), []string{"bd-recs-src"})
	s.Require().NoError(err)
	s.Require().Len(out["bd-recs-src"], 2)
	targets := map[string]types.DependencyType{}
	for _, dep := range out["bd-recs-src"] {
		targets[dep.DependsOnID] = dep.Type
	}
	s.Equal(types.DepBlocks, targets["bd-recs-a"])
	s.Equal(types.DepRelated, targets["bd-recs-b"])
}

func (s *testSuite) depRecsWispRouted() {
	// Wisp source: its deps live in wisp_dependencies and must still come back
	// when queried via the partitioning method.
	s.seedWispRow("bd-recs-wsrc")
	s.seedIssueRow("bd-recs-wtgt")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-recs-wsrc", "bd-recs-wtgt", types.DepBlocks), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))

	out, err := r.GetDependencyRecordsForIssues(s.Ctx(), []string{"bd-recs-wsrc"})
	s.Require().NoError(err)
	s.Require().Len(out["bd-recs-wsrc"], 1, "wisp ID must route to wisp_dependencies and be returned")
	s.Equal("bd-recs-wtgt", out["bd-recs-wsrc"][0].DependsOnID)
}

func (s *testSuite) depRecsMissingID() {
	s.seedIssueRow("bd-recs-mi-real")
	s.seedIssueRow("bd-recs-mi-target")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-recs-mi-real", "bd-recs-mi-target", types.DepBlocks), "tester",
		domain.DepInsertOpts{}))

	out, err := r.GetDependencyRecordsForIssues(s.Ctx(), []string{"bd-recs-mi-real", "bd-recs-mi-missing"})
	s.Require().NoError(err)
	s.NotEmpty(out["bd-recs-mi-real"])
	_, present := out["bd-recs-mi-missing"]
	s.False(present, "non-existent IDs must not appear in the result map")
}

// ---- GetWispDependencyRecordsForIDs ----

func (s *testSuite) depWispRecsEmpty() {
	out, err := s.depRepo().GetWispDependencyRecordsForIDs(s.Ctx(), nil)
	s.Require().NoError(err)
	s.NotNil(out)
	s.Empty(out)
}

func (s *testSuite) depWispRecsForcedTable() {
	s.seedWispRow("bd-wrecs-src")
	s.seedIssueRow("bd-wrecs-a")
	s.seedIssueRow("bd-wrecs-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-wrecs-src", "bd-wrecs-a", types.DepBlocks), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-wrecs-src", "bd-wrecs-b", types.DepRelated), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))

	out, err := r.GetWispDependencyRecordsForIDs(s.Ctx(), []string{"bd-wrecs-src"})
	s.Require().NoError(err)
	s.Require().Len(out["bd-wrecs-src"], 2)
}

func (s *testSuite) depWispRecsIgnoresPerm() {
	// Same ID has a row in dependencies (perm) — the wisp-only fetch must
	// ignore it. Use raw SQL because Insert(UseWispsTable=false) on a wisp ID
	// would FK-fail.
	s.seedIssueRow("bd-wrecs-ig-perm-src")
	s.seedIssueRow("bd-wrecs-ig-tgt")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-wrecs-ig-perm-src", "bd-wrecs-ig-tgt", types.DepBlocks), "tester",
		domain.DepInsertOpts{}))

	// Forced wisp-only fetch using the perm ID must return nothing — there's
	// no wisp_dependencies row for it even though dependencies has one.
	out, err := r.GetWispDependencyRecordsForIDs(s.Ctx(), []string{"bd-wrecs-ig-perm-src"})
	s.Require().NoError(err)
	s.Empty(out["bd-wrecs-ig-perm-src"], "forced wisp-table fetch must ignore perm rows")
}
