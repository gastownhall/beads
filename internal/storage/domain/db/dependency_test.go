package db

import (
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestDependencySQLRepository() {
	s.Run("Insert", func() {
		s.Run("RoundTripVisibleViaList", s.depInsertRoundTrip)
		s.Run("RejectsSelfDependency", s.depInsertSelfDep)
		s.Run("RejectsEmptyIDs", s.depInsertEmptyIDs)
		s.Run("SameTypeIsIdempotentMetadataRefresh", s.depInsertIdempotentSameType)
		s.Run("UsesDeterministicID", s.depInsertUsesDeterministicID)
		s.Run("DifferentTypeIsRejected", s.depInsertConflictingType)
		s.Run("MissingTargetIssueFailsFK", s.depInsertFKViolation)
		s.Run("ThreadIDPersists", s.depInsertThreadID)
	})
	s.Run("Delete", func() {
		s.Run("ReturnsFoundFalseOnMissingEdge", s.depDeleteMissingEdge)
		s.Run("ReturnsTypeAndDependsOnID", s.depDeleteReturnsMetadata)
		s.Run("RemovesRow", s.depDeleteRemovesRow)
		s.Run("RejectsEmptyIDs", s.depDeleteEmptyIDs)
		s.Run("WispRoutesToWispDependencies", s.depDeleteWispRouting)
	})
	s.Run("HasCycle", func() {
		s.Run("StraightLineIsAcyclic", s.depCycleAcyclic)
		s.Run("DirectBackEdgeDetected", s.depCycleDirectBackEdge)
		s.Run("BackEdgeDetected", s.depCycleBackEdge)
		s.Run("NonBlockingEdgesIgnored", s.depCycleIgnoresNonBlocking)
	})
	s.Run("ListByIssueIDs", func() {
		s.Run("EmptySliceReturnsEmptyMaps", s.depListEmpty)
		s.Run("OutgoingOnly", s.depListOutgoing)
		s.Run("IncomingOnly", s.depListIncoming)
		s.Run("BothDirections", s.depListBoth)
		s.Run("TypeFilterApplied", s.depListTypeFilter)
	})
	s.Run("CountsByIssueIDs", func() {
		s.Run("EmptySliceReturnsEmptyMap", s.depCountsEmpty)
		s.Run("CountsBlockingEdgesOnly", s.depCountsBlocksOnly)
		s.Run("ZeroCountsPresentInMap", s.depCountsZeroPresent)
	})
	s.Run("GetBlockingInfo", func() {
		s.Run("EmptyInputReturnsEmptyMaps", s.depBlockingInfoEmpty)
		s.Run("PopulatesBlockedByAndBlocks", s.depBlockingInfoBlockedByAndBlocks)
		s.Run("ParentChildPopulatesParent", s.depBlockingInfoParent)
		s.Run("ClosedBlockerFiltered", s.depBlockingInfoSkipsClosed)
	})
	s.Run("GetBlockingInfoAcrossIssuesAndWisps", func() {
		s.Run("UnionsBothTables", s.depBlockingInfoAcrossUnions)
	})
	s.Run("Wisp", func() {
		s.Run("InsertRoutesToWispDependencies", s.depWispInsertRouting)
		s.Run("ListReadsFromWispDependencies", s.depWispListRouting)
		s.Run("CountsReadFromWispDependencies", s.depWispCountsRouting)
		s.Run("HasCycleSpansBothTables", s.depWispHasCycleCrossTable)
		s.Run("WispDirectBackEdgeDetected", s.depWispDirectBackEdge)
	})
}

func (s *testSuite) depRepo() domain.DependencySQLRepository {
	return NewDependencySQLRepository(s.Runner())
}

func newDep(issueID, dependsOnID string, t types.DependencyType) *types.Dependency {
	return &types.Dependency{
		IssueID:     issueID,
		DependsOnID: dependsOnID,
		Type:        t,
	}
}

// depInsertUsesDeterministicID guards the #4259 fix on the server-mode (use-case)
// insert path: the row must carry the deterministic id derived from
// (issue_id, target), not a random UUID — otherwise the table is merge-unsafe and,
// after the DEFAULT (UUID()) is dropped, the insert fails outright.
func (s *testSuite) depInsertUsesDeterministicID() {
	s.seedIssueRow("bd-dep-det-a")
	s.seedIssueRow("bd-dep-det-b")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-dep-det-a", "bd-dep-det-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	var gotID string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT id FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
		"bd-dep-det-a", "bd-dep-det-b").Scan(&gotID))
	s.Equal(depid.New("bd-dep-det-a", "bd-dep-det-b"), gotID)
}

func (s *testSuite) depInsertRoundTrip() {
	s.seedIssueRow("bd-dep-a")
	s.seedIssueRow("bd-dep-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-dep-a", "bd-dep-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-dep-a"}, domain.DepListOpts{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	s.Require().Len(out.Outgoing["bd-dep-a"], 1)
	s.Equal("bd-dep-b", out.Outgoing["bd-dep-a"][0].DependsOnID)
	s.Equal(types.DepBlocks, out.Outgoing["bd-dep-a"][0].Type)
}

func (s *testSuite) depInsertSelfDep() {
	s.seedIssueRow("bd-dep-self")
	err := s.depRepo().Insert(s.Ctx(), newDep("bd-dep-self", "bd-dep-self", types.DepBlocks), "tester", domain.DepInsertOpts{})
	s.Require().Error(err)
	s.Contains(err.Error(), "cannot depend on itself")
}

func (s *testSuite) depInsertEmptyIDs() {
	r := s.depRepo()
	s.Require().Error(r.Insert(s.Ctx(), newDep("", "bd-x", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().Error(r.Insert(s.Ctx(), newDep("bd-x", "", types.DepBlocks), "tester", domain.DepInsertOpts{}))
}

func (s *testSuite) depInsertIdempotentSameType() {
	s.seedIssueRow("bd-dep-idem-1")
	s.seedIssueRow("bd-dep-idem-2")
	r := s.depRepo()

	dep := newDep("bd-dep-idem-1", "bd-dep-idem-2", types.DepBlocks)
	dep.Metadata = `{"v":1}`
	s.Require().NoError(r.Insert(s.Ctx(), dep, "tester", domain.DepInsertOpts{}))

	// Re-add same edge, new metadata. Should refresh, not error.
	dep.Metadata = `{"v":2}`
	s.Require().NoError(r.Insert(s.Ctx(), dep, "tester", domain.DepInsertOpts{}))

	out, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-dep-idem-1"}, domain.DepListOpts{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	s.Require().Len(out.Outgoing["bd-dep-idem-1"], 1, "duplicate insert should still result in exactly one row")
	s.Equal(`{"v":2}`, out.Outgoing["bd-dep-idem-1"][0].Metadata)
}

func (s *testSuite) depInsertConflictingType() {
	s.seedIssueRow("bd-dep-conf-1")
	s.seedIssueRow("bd-dep-conf-2")
	r := s.depRepo()

	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-dep-conf-1", "bd-dep-conf-2", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	err := r.Insert(s.Ctx(), newDep("bd-dep-conf-1", "bd-dep-conf-2", types.DepRelated), "tester", domain.DepInsertOpts{})
	s.Require().Error(err)
	s.Contains(err.Error(), "already exists with type")
}

func (s *testSuite) depInsertFKViolation() {
	s.seedIssueRow("bd-dep-src")
	err := s.depRepo().Insert(s.Ctx(), newDep("bd-dep-src", "bd-dep-no-such-target", types.DepBlocks), "tester", domain.DepInsertOpts{})
	s.Require().Error(err, "missing target should fail fk_dep_issue_target")
}

func (s *testSuite) depInsertThreadID() {
	s.seedIssueRow("bd-dep-th-1")
	s.seedIssueRow("bd-dep-th-2")
	r := s.depRepo()

	dep := newDep("bd-dep-th-1", "bd-dep-th-2", types.DepRepliesTo)
	dep.ThreadID = "thread-xyz"
	s.Require().NoError(r.Insert(s.Ctx(), dep, "tester", domain.DepInsertOpts{}))

	out, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-dep-th-1"}, domain.DepListOpts{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	s.Require().Len(out.Outgoing["bd-dep-th-1"], 1)
	s.Equal("thread-xyz", out.Outgoing["bd-dep-th-1"][0].ThreadID)
}

func (s *testSuite) depCycleAcyclic() {
	s.seedIssueRow("bd-cy-a")
	s.seedIssueRow("bd-cy-b")
	s.seedIssueRow("bd-cy-c")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cy-a", "bd-cy-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cy-b", "bd-cy-c", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	// Adding a -> c is fine.
	cycle, err := r.HasCycle(s.Ctx(), "bd-cy-a", "bd-cy-c")
	s.Require().NoError(err)
	s.False(cycle)
}

func (s *testSuite) depCycleDirectBackEdge() {
	// Direct (one-hop) back-edge: a blocks b already; adding b -> a closes
	// a 2-cycle. Exercises the indexed point-lookup fast path before the CTE.
	s.seedIssueRow("bd-cy-dir-a")
	s.seedIssueRow("bd-cy-dir-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cy-dir-a", "bd-cy-dir-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	cycle, err := r.HasCycle(s.Ctx(), "bd-cy-dir-b", "bd-cy-dir-a")
	s.Require().NoError(err)
	s.True(cycle, "direct back-edge should detect cycle via fast path")
}

func (s *testSuite) depCycleBackEdge() {
	s.seedIssueRow("bd-cy-back-a")
	s.seedIssueRow("bd-cy-back-b")
	s.seedIssueRow("bd-cy-back-c")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cy-back-a", "bd-cy-back-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cy-back-b", "bd-cy-back-c", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	// Adding c -> a would close the cycle a -> b -> c -> a.
	cycle, err := r.HasCycle(s.Ctx(), "bd-cy-back-c", "bd-cy-back-a")
	s.Require().NoError(err)
	s.True(cycle, "expected back-edge to close a cycle")
}

func (s *testSuite) depCycleIgnoresNonBlocking() {
	s.seedIssueRow("bd-cy-rel-a")
	s.seedIssueRow("bd-cy-rel-b")
	r := s.depRepo()
	// related-only edge — not a blocking type, must not contribute to cycle search.
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cy-rel-a", "bd-cy-rel-b", types.DepRelated), "tester", domain.DepInsertOpts{}))

	cycle, err := r.HasCycle(s.Ctx(), "bd-cy-rel-b", "bd-cy-rel-a")
	s.Require().NoError(err)
	s.False(cycle)
}

func (s *testSuite) depListEmpty() {
	out, err := s.depRepo().ListByIssueIDs(s.Ctx(), nil, domain.DepListOpts{})
	s.Require().NoError(err)
	s.NotNil(out.Outgoing)
	s.NotNil(out.Incoming)
	s.Empty(out.Outgoing)
	s.Empty(out.Incoming)
}

func (s *testSuite) depListOutgoing() {
	s.seedIssueRow("bd-lst-out-1")
	s.seedIssueRow("bd-lst-out-2")
	s.seedIssueRow("bd-lst-out-3")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-lst-out-1", "bd-lst-out-2", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-lst-out-1", "bd-lst-out-3", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-lst-out-1"}, domain.DepListOpts{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	s.Require().Len(out.Outgoing["bd-lst-out-1"], 2)
	s.Empty(out.Incoming, "outgoing-only request should leave Incoming empty")
}

func (s *testSuite) depListIncoming() {
	s.seedIssueRow("bd-lst-in-1")
	s.seedIssueRow("bd-lst-in-2")
	s.seedIssueRow("bd-lst-in-3")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-lst-in-2", "bd-lst-in-1", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-lst-in-3", "bd-lst-in-1", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-lst-in-1"}, domain.DepListOpts{Direction: domain.DepDirectionIn})
	s.Require().NoError(err)
	s.Require().Len(out.Incoming["bd-lst-in-1"], 2)
	s.Empty(out.Outgoing)
}

func (s *testSuite) depListBoth() {
	s.seedIssueRow("bd-lst-bo-mid")
	s.seedIssueRow("bd-lst-bo-up")
	s.seedIssueRow("bd-lst-bo-down")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-lst-bo-up", "bd-lst-bo-mid", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-lst-bo-mid", "bd-lst-bo-down", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-lst-bo-mid"}, domain.DepListOpts{Direction: domain.DepDirectionBoth})
	s.Require().NoError(err)
	s.Len(out.Outgoing["bd-lst-bo-mid"], 1, "mid -> down should be outgoing")
	s.Equal("bd-lst-bo-down", out.Outgoing["bd-lst-bo-mid"][0].DependsOnID)
	s.Len(out.Incoming["bd-lst-bo-mid"], 1, "up -> mid should be incoming")
	s.Equal("bd-lst-bo-up", out.Incoming["bd-lst-bo-mid"][0].IssueID)
}

func (s *testSuite) depListTypeFilter() {
	s.seedIssueRow("bd-lst-typ-a")
	s.seedIssueRow("bd-lst-typ-b")
	s.seedIssueRow("bd-lst-typ-c")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-lst-typ-a", "bd-lst-typ-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-lst-typ-a", "bd-lst-typ-c", types.DepRelated), "tester", domain.DepInsertOpts{}))

	out, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-lst-typ-a"}, domain.DepListOpts{
		Direction: domain.DepDirectionOut,
		Types:     []types.DependencyType{types.DepBlocks},
	})
	s.Require().NoError(err)
	s.Require().Len(out.Outgoing["bd-lst-typ-a"], 1)
	s.Equal("bd-lst-typ-b", out.Outgoing["bd-lst-typ-a"][0].DependsOnID)
}

func (s *testSuite) depCountsEmpty() {
	out, err := s.depRepo().CountsByIssueIDs(s.Ctx(), nil, domain.DepCountsOpts{})
	s.Require().NoError(err)
	s.NotNil(out)
	s.Empty(out)
}

func (s *testSuite) depCountsBlocksOnly() {
	s.seedIssueRow("bd-cnt-mid")
	s.seedIssueRow("bd-cnt-out-1")
	s.seedIssueRow("bd-cnt-out-2")
	s.seedIssueRow("bd-cnt-in-1")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cnt-mid", "bd-cnt-out-1", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cnt-mid", "bd-cnt-out-2", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	// Non-blocking outgoing — must not be counted.
	s.seedIssueRow("bd-cnt-rel-tgt")
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cnt-mid", "bd-cnt-rel-tgt", types.DepRelated), "tester", domain.DepInsertOpts{}))
	// Incoming blocking edge.
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-cnt-in-1", "bd-cnt-mid", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.CountsByIssueIDs(s.Ctx(), []string{"bd-cnt-mid"}, domain.DepCountsOpts{})
	s.Require().NoError(err)
	s.Require().NotNil(out["bd-cnt-mid"])
	s.Equal(2, out["bd-cnt-mid"].DependencyCount, "outgoing blocks only")
	s.Equal(1, out["bd-cnt-mid"].DependentCount, "incoming blocks only")
}

func (s *testSuite) depCountsZeroPresent() {
	s.seedIssueRow("bd-cnt-zero")
	out, err := s.depRepo().CountsByIssueIDs(s.Ctx(), []string{"bd-cnt-zero"}, domain.DepCountsOpts{})
	s.Require().NoError(err)
	s.Require().NotNil(out["bd-cnt-zero"], "issues with zero deps should still appear with zero counts")
	s.Equal(0, out["bd-cnt-zero"].DependencyCount)
	s.Equal(0, out["bd-cnt-zero"].DependentCount)
}

func (s *testSuite) depWispInsertRouting() {
	// Source is a wisp; target is a permanent issue. wisp_dependencies has
	// fk_wisp_dep_issue (issue_id -> wisps) and fk_wisp_dep_issue_target
	// (depends_on_issue_id -> issues).
	s.seedWispRow("bd-dep-wisp-src")
	s.seedIssueRow("bd-dep-wisp-tgt")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-dep-wisp-src", "bd-dep-wisp-tgt", types.DepBlocks), "tester",
		domain.DepInsertOpts{UseWispsTable: true},
	))

	var wispCount, permCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?", "bd-dep-wisp-src").Scan(&wispCount))
	s.Equal(1, wispCount)
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", "bd-dep-wisp-src").Scan(&permCount))
	s.Equal(0, permCount, "wisp-routed insert must not write to dependencies")
}

func (s *testSuite) depWispListRouting() {
	s.seedWispRow("bd-dep-wlist-src")
	s.seedIssueRow("bd-dep-wlist-a")
	s.seedIssueRow("bd-dep-wlist-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-dep-wlist-src", "bd-dep-wlist-a", types.DepBlocks), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-dep-wlist-src", "bd-dep-wlist-b", types.DepRelated), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))

	out, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-dep-wlist-src"},
		domain.DepListOpts{Direction: domain.DepDirectionOut, UseWispsTable: true})
	s.Require().NoError(err)
	s.Require().Len(out.Outgoing["bd-dep-wlist-src"], 2)

	// Same query against the permanent table returns nothing.
	empty, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-dep-wlist-src"},
		domain.DepListOpts{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	s.Empty(empty.Outgoing)
}

func (s *testSuite) depWispCountsRouting() {
	s.seedWispRow("bd-dep-wcnt-src")
	s.seedIssueRow("bd-dep-wcnt-a")
	s.seedIssueRow("bd-dep-wcnt-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-dep-wcnt-src", "bd-dep-wcnt-a", types.DepBlocks), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-dep-wcnt-src", "bd-dep-wcnt-b", types.DepBlocks), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))

	out, err := r.CountsByIssueIDs(s.Ctx(), []string{"bd-dep-wcnt-src"}, domain.DepCountsOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Require().NotNil(out["bd-dep-wcnt-src"])
	s.Equal(2, out["bd-dep-wcnt-src"].DependencyCount)

	// Permanent-table count is zero for the same source.
	permOut, err := r.CountsByIssueIDs(s.Ctx(), []string{"bd-dep-wcnt-src"}, domain.DepCountsOpts{})
	s.Require().NoError(err)
	s.Require().NotNil(permOut["bd-dep-wcnt-src"])
	s.Equal(0, permOut["bd-dep-wcnt-src"].DependencyCount)
}

func (s *testSuite) depWispHasCycleCrossTable() {
	// Cross-table closure: issue a (perm) blocks wisp s (wisp_dependencies),
	// then wisp s blocks issue b (wisp_dependencies). Adding b -> a (perm)
	// would close a cycle through both tables.
	s.seedIssueRow("bd-dep-cx-a")
	s.seedIssueRow("bd-dep-cx-b")
	s.seedWispRow("bd-dep-cx-s")

	r := s.depRepo()
	// a -> s: source a is permanent, target is a wisp. Store it explicitly in
	// depends_on_wisp_id to exercise typed-target traversal.
	_, err := s.Runner().ExecContext(s.Ctx(), `
		INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by, metadata)
		VALUES (UUID(), ?, ?, 'blocks', NOW(), 'tester', '{}')
	`, "bd-dep-cx-a", "bd-dep-cx-s")
	s.Require().NoError(err)
	// s -> b: source s is wisp, target is permanent. Stored in wisp_dependencies.
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-dep-cx-s", "bd-dep-cx-b", types.DepBlocks), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))

	// HasCycle traverses both tables and resolves all typed target columns, so
	// b -> a would close b -> a -> s -> b.
	cycle, err := r.HasCycle(s.Ctx(), "bd-dep-cx-b", "bd-dep-cx-a")
	s.Require().NoError(err)
	s.True(cycle, "wisp-target edges must participate in combined cycle detection")
}

func (s *testSuite) depBlockingInfoEmpty() {
	info, err := s.depRepo().GetBlockingInfo(s.Ctx(), nil, domain.DepListOpts{})
	s.Require().NoError(err)
	s.NotNil(info.BlockedBy)
	s.NotNil(info.Blocks)
	s.NotNil(info.Parent)
	s.Empty(info.BlockedBy)
	s.Empty(info.Blocks)
	s.Empty(info.Parent)
}

func (s *testSuite) depBlockingInfoBlockedByAndBlocks() {
	s.seedIssueRow("bd-bi-mid")
	s.seedIssueRow("bd-bi-up")
	s.seedIssueRow("bd-bi-down")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-bi-mid", "bd-bi-up", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-bi-down", "bd-bi-mid", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	info, err := r.GetBlockingInfo(s.Ctx(), []string{"bd-bi-mid"}, domain.DepListOpts{})
	s.Require().NoError(err)
	s.Equal([]string{"bd-bi-up"}, info.BlockedBy["bd-bi-mid"])
	s.Equal([]string{"bd-bi-down"}, info.Blocks["bd-bi-mid"])
	s.Empty(info.Parent)
}

func (s *testSuite) depBlockingInfoParent() {
	s.seedIssueRow("bd-bi-child")
	s.seedIssueRow("bd-bi-parent")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-bi-child", "bd-bi-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))

	info, err := r.GetBlockingInfo(s.Ctx(), []string{"bd-bi-child"}, domain.DepListOpts{})
	s.Require().NoError(err)
	s.Equal("bd-bi-parent", info.Parent["bd-bi-child"])
	s.Empty(info.BlockedBy, "parent-child must not appear in BlockedBy")
}

func (s *testSuite) depBlockingInfoSkipsClosed() {
	s.seedIssueRow("bd-bi-cls-mid")
	s.seedIssueRow("bd-bi-cls-blocker")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-bi-cls-mid", "bd-bi-cls-blocker", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	_, err := s.Runner().ExecContext(s.Ctx(),
		"UPDATE issues SET status = ? WHERE id = ?", string(types.StatusClosed), "bd-bi-cls-blocker")
	s.Require().NoError(err)

	info, err := r.GetBlockingInfo(s.Ctx(), []string{"bd-bi-cls-mid"}, domain.DepListOpts{})
	s.Require().NoError(err)
	s.Empty(info.BlockedBy["bd-bi-cls-mid"], "closed blockers should be filtered out")
}

func (s *testSuite) depBlockingInfoAcrossUnions() {
	s.seedIssueRow("bd-bi-x-target")
	s.seedIssueRow("bd-bi-x-permblocker")
	s.seedWispRow("bd-bi-x-wispblocker")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-bi-x-target", "bd-bi-x-permblocker", types.DepBlocks), "tester",
		domain.DepInsertOpts{}))
	_, err := s.Runner().ExecContext(s.Ctx(), `
		INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by, metadata)
		VALUES (UUID(), ?, ?, 'blocks', NOW(), 'tester', '{}')
	`, "bd-bi-x-target", "bd-bi-x-wispblocker")
	s.Require().NoError(err)

	info, err := r.GetBlockingInfoAcrossIssuesAndWisps(s.Ctx(), []string{"bd-bi-x-target"})
	s.Require().NoError(err)
	s.ElementsMatch([]string{"bd-bi-x-permblocker", "bd-bi-x-wispblocker"}, info.BlockedBy["bd-bi-x-target"])
}

func (s *testSuite) depWispDirectBackEdge() {
	s.seedWispRow("bd-dep-wd-s")
	s.seedIssueRow("bd-dep-wd-t")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-dep-wd-s", "bd-dep-wd-t", types.DepBlocks), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))

	cycle, err := r.HasCycle(s.Ctx(), "bd-dep-wd-t", "bd-dep-wd-s")
	s.Require().NoError(err)
	s.True(cycle, "fast path must probe wisp_dependencies too")
}

func (s *testSuite) depDeleteMissingEdge() {
	s.seedIssueRow("bd-dep-del-miss-a")
	s.seedIssueRow("bd-dep-del-miss-b")
	r := s.depRepo()

	res, err := r.Delete(s.Ctx(), "bd-dep-del-miss-a", "bd-dep-del-miss-b", "tester", domain.DepInsertOpts{})
	s.Require().NoError(err, "Delete on a non-existent edge must succeed (mirrors RemoveDependencyInTx)")
	s.False(res.Found, "Found must be false so callers like Reparent can distinguish no-op from removal")
}

func (s *testSuite) depDeleteReturnsMetadata() {
	s.seedIssueRow("bd-dep-del-meta-a")
	s.seedIssueRow("bd-dep-del-meta-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-dep-del-meta-a", "bd-dep-del-meta-b", types.DepParentChild), "tester", domain.DepInsertOpts{}))

	res, err := r.Delete(s.Ctx(), "bd-dep-del-meta-a", "bd-dep-del-meta-b", "tester", domain.DepInsertOpts{})
	s.Require().NoError(err)
	s.True(res.Found)
	s.Equal(types.DepParentChild, res.Type)
	s.Equal("bd-dep-del-meta-b", res.DependsOnID)
}

func (s *testSuite) depDeleteRemovesRow() {
	s.seedIssueRow("bd-dep-del-row-a")
	s.seedIssueRow("bd-dep-del-row-b")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-dep-del-row-a", "bd-dep-del-row-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	_, err := r.Delete(s.Ctx(), "bd-dep-del-row-a", "bd-dep-del-row-b", "tester", domain.DepInsertOpts{})
	s.Require().NoError(err)

	out, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-dep-del-row-a"}, domain.DepListOpts{Direction: domain.DepDirectionOut})
	s.Require().NoError(err)
	s.Empty(out.Outgoing["bd-dep-del-row-a"], "deleted dep must disappear from outgoing list")
}

func (s *testSuite) depDeleteEmptyIDs() {
	r := s.depRepo()
	_, err := r.Delete(s.Ctx(), "", "bd-x", "tester", domain.DepInsertOpts{})
	s.Require().Error(err)
	_, err = r.Delete(s.Ctx(), "bd-x", "", "tester", domain.DepInsertOpts{})
	s.Require().Error(err)
}

func (s *testSuite) depDeleteWispRouting() {
	s.seedIssueRow("bd-dep-del-wisp-issuesrc")
	s.seedIssueRow("bd-dep-del-wisp-issuetgt")
	s.seedWispRow("bd-dep-del-wisp-wispsrc")
	s.seedWispRow("bd-dep-del-wisp-wisptgt")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-dep-del-wisp-issuesrc", "bd-dep-del-wisp-issuetgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-dep-del-wisp-wispsrc", "bd-dep-del-wisp-wisptgt", types.DepBlocks), "tester", domain.DepInsertOpts{UseWispsTable: true}))

	res, err := r.Delete(s.Ctx(), "bd-dep-del-wisp-wispsrc", "bd-dep-del-wisp-wisptgt", "tester", domain.DepInsertOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.True(res.Found)

	var permCount, wispCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", "bd-dep-del-wisp-issuesrc").Scan(&permCount))
	s.Equal(1, permCount, "wisp-routed Delete must not touch the dependencies table")
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?", "bd-dep-del-wisp-wispsrc").Scan(&wispCount))
	s.Equal(0, wispCount, "wisp-routed Delete must remove from wisp_dependencies")
}
