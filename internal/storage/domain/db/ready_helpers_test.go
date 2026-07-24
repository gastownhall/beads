package db

import (
	"context"
	"sort"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) resetDB() {
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, "CALL DOLT_RESET('--hard', ?)", s.baselineCommit)
	s.Require().NoError(err)
	for _, table := range []string{"wisp_dependencies", "wisp_labels", "wisp_events", "wisp_child_counters", "wisp_comments", "wisps"} {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			s.T().Logf("clear %s: %v", table, err)
		}
	}
}

func (s *testSuite) TestIssueClaimReadyIssue() {
	s.Run("ClaimsFirstReady", s.claimReadyClaimsFirst)
	s.Run("MarksInProgressAndAssigns", s.claimReadySetsState)
	s.Run("NoReadyReturnsNil", s.claimReadyEmpty)
	s.Run("SkipsAlreadyClaimedByOther", s.claimReadySkipsForeign)
	s.Run("RespectsPriorityFilter", s.claimReadyPriorityFilter)
	s.Run("ExcludesBlocked", s.claimReadyExcludesBlocked)
}

func (s *testSuite) TestIssueClaimReadyWisp() {
	s.Run("ClaimsFirstReady", s.claimReadyWispClaimsFirst)
	s.Run("NoReadyReturnsNil", s.claimReadyWispEmpty)
}

func (s *testSuite) TestIssueGetBlockedIssues() {
	s.Run("EmptyDBReturnsNil", s.blockedEmpty)
	s.Run("ReturnsBlockedWithBlockers", s.blockedReturnsBlockers)
	s.Run("ExcludesClosed", s.blockedExcludesClosed)
	s.Run("UnblocksWhenBlockerCloses", s.blockedUnblocksOnClose)
	s.Run("MultipleBlockersAggregated", s.blockedMultipleAggregated)
	s.Run("InheritedThroughParentChild", s.blockedInheritedThroughParent)
	s.Run("ParentIDFilterRestrictsToDescendants", s.blockedParentIDFilter)
}

func (s *testSuite) TestIssueGetStatistics() {
	s.Run("EmptyDBReturnsZeroes", s.statsEmpty)
	s.Run("CountsByStatus", s.statsCountsByStatus)
	s.Run("CountsBlocked", s.statsCountsBlocked)
	s.Run("CountsPinned", s.statsCountsPinned)
	s.Run("ReadyDerivedFromOpenMinusBlocked", s.statsReadyDerived)
	s.Run("ReadyClampedAtZero", s.statsReadyClamped)
}

func (s *testSuite) TestDependencyDetectCycles() {
	s.Run("NoEdgesReturnsEmpty", s.cyclesEmpty)
	s.Run("AcyclicReturnsEmpty", s.cyclesAcyclic)
	s.Run("TwoNodeCycleDetected", s.cyclesTwoNode)
	s.Run("ThreeNodeCycleDetected", s.cyclesThreeNode)
	s.Run("IgnoresNonBlockingDepTypes", s.cyclesIgnoresParentChild)
	s.Run("ConditionalBlocksFormsCycle", s.cyclesConditionalBlocks)
}

// ---------- ClaimReadyIssue ----------

func (s *testSuite) claimReadyClaimsFirst() {
	r := s.issueRepo()
	a := newTestIssue("bd-clr-a", "alpha")
	a.Priority = 1
	s.Require().NoError(r.Insert(s.Ctx(), a, "tester", domain.InsertIssueOpts{}))
	b := newTestIssue("bd-clr-b", "bravo")
	b.Priority = 2
	s.Require().NoError(r.Insert(s.Ctx(), b, "tester", domain.InsertIssueOpts{}))

	out, err := r.ClaimReadyIssue(s.Ctx(), types.WorkFilter{SortPolicy: types.SortPolicyPriority}, "alice")
	s.Require().NoError(err)
	s.Require().NotNil(out)
	s.Equal("bd-clr-a", out.ID, "first ready by priority should be claimed")
}

func (s *testSuite) claimReadySetsState() {
	s.resetDB()
	r := s.issueRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue("bd-clr-state", "x"), "tester", domain.InsertIssueOpts{}))

	out, err := r.ClaimReadyIssue(s.Ctx(), types.WorkFilter{}, "alice")
	s.Require().NoError(err)
	s.Require().NotNil(out)
	s.Equal("bd-clr-state", out.ID)

	got, err := r.Get(s.Ctx(), "bd-clr-state", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal(types.StatusInProgress, got.Status)
	s.Equal("alice", got.Assignee)
	s.NotNil(got.StartedAt, "started_at must be set on first claim")
}

func (s *testSuite) claimReadyEmpty() {
	s.resetDB()
	r := s.issueRepo()
	out, err := r.ClaimReadyIssue(s.Ctx(), types.WorkFilter{}, "alice")
	s.Require().NoError(err)
	s.Nil(out)
}

func (s *testSuite) claimReadySkipsForeign() {
	r := s.issueRepo()
	taken := newTestIssue("bd-clr-skip-taken", "taken")
	taken.Status = types.StatusInProgress
	taken.Assignee = "bob"
	s.Require().NoError(r.Insert(s.Ctx(), taken, "tester", domain.InsertIssueOpts{}))
	free := newTestIssue("bd-clr-skip-free", "free")
	free.Priority = 2
	s.Require().NoError(r.Insert(s.Ctx(), free, "tester", domain.InsertIssueOpts{}))

	out, err := r.ClaimReadyIssue(s.Ctx(), types.WorkFilter{SortPolicy: types.SortPolicyPriority}, "alice")
	s.Require().NoError(err)
	s.Require().NotNil(out)
	s.Equal("bd-clr-skip-free", out.ID, "must pass over in-progress issue owned by another actor")
}

func (s *testSuite) claimReadyPriorityFilter() {
	r := s.issueRepo()
	low := newTestIssue("bd-clr-pri-low", "low")
	low.Priority = 3
	s.Require().NoError(r.Insert(s.Ctx(), low, "tester", domain.InsertIssueOpts{}))
	high := newTestIssue("bd-clr-pri-high", "high")
	high.Priority = 1
	s.Require().NoError(r.Insert(s.Ctx(), high, "tester", domain.InsertIssueOpts{}))

	p := 1
	out, err := r.ClaimReadyIssue(s.Ctx(), types.WorkFilter{Priority: &p}, "alice")
	s.Require().NoError(err)
	s.Require().NotNil(out)
	s.Equal("bd-clr-pri-high", out.ID)
}

func (s *testSuite) claimReadyExcludesBlocked() {
	r := s.issueRepo()
	dr := s.depRepo()
	src := newTestIssue("bd-clr-blk-src", "blocked-src")
	src.Priority = 1
	s.Require().NoError(r.Insert(s.Ctx(), src, "tester", domain.InsertIssueOpts{}))
	tgt := newTestIssue("bd-clr-blk-tgt", "blocker")
	tgt.Priority = 2
	s.Require().NoError(r.Insert(s.Ctx(), tgt, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-clr-blk-src", "bd-clr-blk-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.ClaimReadyIssue(s.Ctx(), types.WorkFilter{SortPolicy: types.SortPolicyPriority}, "alice")
	s.Require().NoError(err)
	s.Require().NotNil(out)
	s.Equal("bd-clr-blk-tgt", out.ID, "blocked issue must be skipped even if higher priority")
}

// ---------- ClaimReadyWisp ----------
// ClaimReadyWisp currently delegates to ClaimReadyIssueInTx (which already spans
// issues + wisps via GetReadyWorkInTx). These smoke tests guard the delegation.

func (s *testSuite) claimReadyWispClaimsFirst() {
	r := s.issueRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue("bd-clrw-a", "alpha"), "tester", domain.InsertIssueOpts{}))
	out, err := r.ClaimReadyWisp(s.Ctx(), types.WorkFilter{}, "alice")
	s.Require().NoError(err)
	s.Require().NotNil(out)
	s.Equal("bd-clrw-a", out.ID)
}

func (s *testSuite) claimReadyWispEmpty() {
	r := s.issueRepo()
	out, err := r.ClaimReadyWisp(s.Ctx(), types.WorkFilter{}, "alice")
	s.Require().NoError(err)
	s.Nil(out)
}

// ---------- GetBlockedIssues ----------

func (s *testSuite) blockedEmpty() {
	r := s.issueRepo()
	out, err := r.GetBlockedIssues(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) blockedReturnsBlockers() {
	r := s.issueRepo()
	dr := s.depRepo()
	src := newTestIssue("bd-bli-src", "blocked")
	s.Require().NoError(r.Insert(s.Ctx(), src, "tester", domain.InsertIssueOpts{}))
	tgt := newTestIssue("bd-bli-tgt", "blocker")
	s.Require().NoError(r.Insert(s.Ctx(), tgt, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-bli-src", "bd-bli-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.GetBlockedIssues(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.Require().Len(out, 1)
	s.Equal("bd-bli-src", out[0].ID)
	s.Equal(1, out[0].BlockedByCount)
	s.Equal([]string{"bd-bli-tgt"}, out[0].BlockedBy)
}

func (s *testSuite) blockedExcludesClosed() {
	s.resetDB()
	r := s.issueRepo()
	dr := s.depRepo()
	src := newTestIssue("bd-blc-src", "closed-blocked")
	s.Require().NoError(r.Insert(s.Ctx(), src, "tester", domain.InsertIssueOpts{}))
	tgt := newTestIssue("bd-blc-tgt", "blocker")
	s.Require().NoError(r.Insert(s.Ctx(), tgt, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-blc-src", "bd-blc-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Update(s.Ctx(), "bd-blc-src",
		map[string]any{"status": string(types.StatusClosed)}, "tester", domain.IssueTableOpts{}))

	out, err := r.GetBlockedIssues(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.NotContains(blockedIDs(out), "bd-blc-src",
		"closed issues must never appear in blocked listing")
}

func (s *testSuite) blockedUnblocksOnClose() {
	s.resetDB()
	r := s.issueRepo()
	dr := s.depRepo()
	src := newTestIssue("bd-bluc-src", "blocked")
	s.Require().NoError(r.Insert(s.Ctx(), src, "tester", domain.InsertIssueOpts{}))
	tgt := newTestIssue("bd-bluc-tgt", "blocker")
	s.Require().NoError(r.Insert(s.Ctx(), tgt, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-bluc-src", "bd-bluc-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	s.Require().NoError(r.Update(s.Ctx(), "bd-bluc-tgt",
		map[string]any{"status": string(types.StatusClosed)}, "tester", domain.IssueTableOpts{}))

	out, err := r.GetBlockedIssues(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.NotContains(blockedIDs(out), "bd-bluc-src",
		"closing the blocker must clear the blocked listing")
}

func (s *testSuite) blockedMultipleAggregated() {
	s.resetDB()
	r := s.issueRepo()
	dr := s.depRepo()
	src := newTestIssue("bd-blm-src", "double-blocked")
	s.Require().NoError(r.Insert(s.Ctx(), src, "tester", domain.InsertIssueOpts{}))
	a := newTestIssue("bd-blm-a", "blocker-a")
	s.Require().NoError(r.Insert(s.Ctx(), a, "tester", domain.InsertIssueOpts{}))
	b := newTestIssue("bd-blm-b", "blocker-b")
	s.Require().NoError(r.Insert(s.Ctx(), b, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-blm-src", "bd-blm-a", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-blm-src", "bd-blm-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.GetBlockedIssues(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	var entry *types.BlockedIssue
	for _, bi := range out {
		if bi.ID == "bd-blm-src" {
			entry = bi
			break
		}
	}
	s.Require().NotNil(entry)
	s.Equal(2, entry.BlockedByCount)
	got := append([]string(nil), entry.BlockedBy...)
	sort.Strings(got)
	s.Equal([]string{"bd-blm-a", "bd-blm-b"}, got)
}

func (s *testSuite) blockedInheritedThroughParent() {
	r := s.issueRepo()
	dr := s.depRepo()
	parent := newTestIssue("bd-blp-parent", "parent")
	s.Require().NoError(r.Insert(s.Ctx(), parent, "tester", domain.InsertIssueOpts{}))
	child := newTestIssue("bd-blp-child", "child")
	s.Require().NoError(r.Insert(s.Ctx(), child, "tester", domain.InsertIssueOpts{}))
	blocker := newTestIssue("bd-blp-blocker", "blocker")
	s.Require().NoError(r.Insert(s.Ctx(), blocker, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-blp-parent", "bd-blp-blocker", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-blp-child", "bd-blp-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))

	out, err := r.GetBlockedIssues(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	gotIDs := blockedIDs(out)
	s.Contains(gotIDs, "bd-blp-parent")
	s.Contains(gotIDs, "bd-blp-child", "child inherits its blocked state from its parent")

	for _, bi := range out {
		if bi.ID == "bd-blp-child" {
			s.Equal([]string{"bd-blp-parent"}, bi.BlockedBy,
				"inherited blocker should be the parent itself")
		}
	}
}

func (s *testSuite) blockedParentIDFilter() {
	r := s.issueRepo()
	dr := s.depRepo()
	parent := newTestIssue("bd-blf-parent", "parent")
	s.Require().NoError(r.Insert(s.Ctx(), parent, "tester", domain.InsertIssueOpts{}))
	insideChild := newTestIssue("bd-blf-parent.1", "inside")
	s.Require().NoError(r.Insert(s.Ctx(), insideChild, "tester", domain.InsertIssueOpts{}))
	outside := newTestIssue("bd-blf-out", "outside")
	s.Require().NoError(r.Insert(s.Ctx(), outside, "tester", domain.InsertIssueOpts{}))
	blocker := newTestIssue("bd-blf-blocker", "blocker")
	s.Require().NoError(r.Insert(s.Ctx(), blocker, "tester", domain.InsertIssueOpts{}))

	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-blf-parent.1", "bd-blf-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-blf-parent.1", "bd-blf-blocker", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-blf-out", "bd-blf-blocker", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	pid := "bd-blf-parent"
	out, err := r.GetBlockedIssues(s.Ctx(), types.WorkFilter{ParentID: &pid})
	s.Require().NoError(err)
	gotIDs := blockedIDs(out)
	s.Contains(gotIDs, "bd-blf-parent.1")
	s.NotContains(gotIDs, "bd-blf-out", "ParentID filter must restrict to descendants")
}

// ---------- GetStatistics ----------

func (s *testSuite) statsEmpty() {
	s.resetDB()
	r := s.issueRepo()
	out, err := r.GetStatistics(s.Ctx())
	s.Require().NoError(err)
	s.Equal(0, out.TotalIssues)
	s.Equal(0, out.OpenIssues)
	s.Equal(0, out.InProgressIssues)
	s.Equal(0, out.ClosedIssues)
	s.Require().NotNil(out.BlockedIssues)
	s.Equal(0, *out.BlockedIssues)
	s.Equal(0, out.DeferredIssues)
	s.Equal(0, out.PinnedIssues)
	s.Require().NotNil(out.ReadyIssues)
	s.Equal(0, *out.ReadyIssues)
}

func (s *testSuite) statsCountsByStatus() {
	s.resetDB()
	r := s.issueRepo()
	mk := func(id string, status types.Status) {
		iss := newTestIssue(id, string(status))
		iss.Status = status
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
	}
	mk("bd-st-o1", types.StatusOpen)
	mk("bd-st-o2", types.StatusOpen)
	mk("bd-st-ip", types.StatusInProgress)
	mk("bd-st-c", types.StatusClosed)
	mk("bd-st-d", types.StatusDeferred)

	out, err := r.GetStatistics(s.Ctx())
	s.Require().NoError(err)
	s.Equal(5, out.TotalIssues)
	s.Equal(2, out.OpenIssues)
	s.Equal(1, out.InProgressIssues)
	s.Equal(1, out.ClosedIssues)
	s.Equal(1, out.DeferredIssues)
}

func (s *testSuite) statsCountsBlocked() {
	s.resetDB()
	r := s.issueRepo()
	dr := s.depRepo()
	src := newTestIssue("bd-stb-src", "blocked")
	s.Require().NoError(r.Insert(s.Ctx(), src, "tester", domain.InsertIssueOpts{}))
	tgt := newTestIssue("bd-stb-tgt", "blocker")
	s.Require().NoError(r.Insert(s.Ctx(), tgt, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-stb-src", "bd-stb-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.GetStatistics(s.Ctx())
	s.Require().NoError(err)
	s.Require().NotNil(out.BlockedIssues)
	s.Equal(1, *out.BlockedIssues)
}

func (s *testSuite) statsCountsPinned() {
	s.resetDB()
	r := s.issueRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue("bd-stp-1", "x"), "tester", domain.InsertIssueOpts{}))
	_, err := s.Runner().ExecContext(s.Ctx(), "UPDATE issues SET pinned = 1 WHERE id = ?", "bd-stp-1")
	s.Require().NoError(err)

	out, err := r.GetStatistics(s.Ctx())
	s.Require().NoError(err)
	s.Equal(1, out.PinnedIssues)
}

func (s *testSuite) statsReadyDerived() {
	s.resetDB()
	r := s.issueRepo()
	dr := s.depRepo()
	for _, id := range []string{"bd-srd-a", "bd-srd-b", "bd-srd-c"} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, id), "tester", domain.InsertIssueOpts{}))
	}
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-srd-a", "bd-srd-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.GetStatistics(s.Ctx())
	s.Require().NoError(err)
	s.Equal(3, out.OpenIssues)
	s.Require().NotNil(out.BlockedIssues)
	s.Equal(1, *out.BlockedIssues)
	s.Require().NotNil(out.ReadyIssues)
	s.Equal(2, *out.ReadyIssues, "ready = open - blocked")
}

func (s *testSuite) statsReadyClamped() {
	s.resetDB()
	r := s.issueRepo()
	iss := newTestIssue("bd-src-c", "closed but somehow flagged")
	iss.Status = types.StatusClosed
	s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
	_, err := s.Runner().ExecContext(s.Ctx(), "UPDATE issues SET is_blocked = 1 WHERE id = ?", "bd-src-c")
	s.Require().NoError(err)

	out, err := r.GetStatistics(s.Ctx())
	s.Require().NoError(err)
	s.Require().NotNil(out.ReadyIssues)
	s.GreaterOrEqual(*out.ReadyIssues, 0, "ready must never go negative")
}

// ---------- DetectCycles ----------

func (s *testSuite) cyclesEmpty() {
	s.resetDB()
	out, err := s.depRepo().DetectCycles(s.Ctx())
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) cyclesAcyclic() {
	s.resetDB()
	r := s.issueRepo()
	dr := s.depRepo()
	for _, id := range []string{"bd-cyA-a", "bd-cyA-b", "bd-cyA-c"} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, id), "tester", domain.InsertIssueOpts{}))
	}
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-cyA-a", "bd-cyA-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-cyA-b", "bd-cyA-c", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := dr.DetectCycles(s.Ctx())
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) cyclesTwoNode() {
	s.resetDB()
	r := s.issueRepo()
	dr := s.depRepo()
	for _, id := range []string{"bd-cy2-a", "bd-cy2-b"} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, id), "tester", domain.InsertIssueOpts{}))
	}
	// Explicit CycleValidated bypasses the defensive repository check so this
	// legacy-cycle reporting test can plant a cycle directly.
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-cy2-a", "bd-cy2-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-cy2-b", "bd-cy2-a", types.DepBlocks), "tester", domain.DepInsertOpts{CycleValidated: true}))

	out, err := dr.DetectCycles(s.Ctx())
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(len(out), 1, "two-node cycle must be detected")
	ids := cycleNodeIDs(out[0])
	s.Contains(ids, "bd-cy2-a")
	s.Contains(ids, "bd-cy2-b")
}

func (s *testSuite) cyclesThreeNode() {
	s.resetDB()
	r := s.issueRepo()
	dr := s.depRepo()
	for _, id := range []string{"bd-cy3-a", "bd-cy3-b", "bd-cy3-c"} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, id), "tester", domain.InsertIssueOpts{}))
	}
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-cy3-a", "bd-cy3-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-cy3-b", "bd-cy3-c", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-cy3-c", "bd-cy3-a", types.DepBlocks), "tester", domain.DepInsertOpts{CycleValidated: true}))

	out, err := dr.DetectCycles(s.Ctx())
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(len(out), 1)
	ids := cycleNodeIDs(out[0])
	for _, want := range []string{"bd-cy3-a", "bd-cy3-b", "bd-cy3-c"} {
		s.Contains(ids, want)
	}
}

func (s *testSuite) cyclesIgnoresParentChild() {
	s.resetDB()
	r := s.issueRepo()
	dr := s.depRepo()
	for _, id := range []string{"bd-cypc-a", "bd-cypc-b"} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, id), "tester", domain.InsertIssueOpts{}))
	}
	// Parent-child is not a blocking dep type — must not register as a cycle.
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-cypc-a", "bd-cypc-b", types.DepParentChild), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-cypc-b", "bd-cypc-a", types.DepParentChild), "tester", domain.DepInsertOpts{CycleValidated: true}))

	out, err := dr.DetectCycles(s.Ctx())
	s.Require().NoError(err)
	s.Empty(out, "parent-child edges must be excluded from cycle detection")
}

func (s *testSuite) cyclesConditionalBlocks() {
	s.resetDB()
	r := s.issueRepo()
	dr := s.depRepo()
	for _, id := range []string{"bd-cycb-a", "bd-cycb-b"} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, id), "tester", domain.InsertIssueOpts{}))
	}
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-cycb-a", "bd-cycb-b", types.DepConditionalBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dr.Insert(s.Ctx(),
		newDep("bd-cycb-b", "bd-cycb-a", types.DepBlocks), "tester", domain.DepInsertOpts{CycleValidated: true}))

	out, err := dr.DetectCycles(s.Ctx())
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(len(out), 1, "conditional-blocks must participate in cycle detection")
}

func blockedIDs(in []*types.BlockedIssue) []string {
	out := make([]string, 0, len(in))
	for _, bi := range in {
		out = append(out, bi.ID)
	}
	return out
}

func cycleNodeIDs(cycle []*types.Issue) []string {
	out := make([]string, 0, len(cycle))
	for _, iss := range cycle {
		out = append(out, iss.ID)
	}
	return out
}
