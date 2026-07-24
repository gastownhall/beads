package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueSQLRepositoryDelete() {
	s.Run("Delete", func() {
		s.Run("RemovesRow", s.issueDeleteRemovesRow)
		s.Run("MissingIDReturnsError", s.issueDeleteMissing)
		s.Run("RoutesToWispsTable", s.issueDeleteWispRouting)
	})
	s.Run("DeleteByIDs", func() {
		s.Run("EmptySliceIsZero", s.issueDeleteByIDsEmpty)
		s.Run("BulkRemovesAndReturnsCount", s.issueDeleteByIDsBulk)
		s.Run("RoutesToWispsTable", s.issueDeleteByIDsWispRouting)
		s.Run("MissingIDsAreSkipped", s.issueDeleteByIDsMissing)
	})
	s.Run("PartitionWispIDs", func() {
		s.Run("EmptyInputReturnsNil", s.issuePartitionEmpty)
		s.Run("SplitsByTable", s.issuePartitionSplits)
	})
	s.Run("FindAllDependents", func() {
		s.Run("EmptyInputReturnsEmpty", s.issueFindAllDependentsEmpty)
		s.Run("TransitiveAcrossDepTypes", s.issueFindAllDependentsTransitive)
		s.Run("IncludesRootIDs", s.issueFindAllDependentsIncludesRoots)
	})
	s.Run("AffectedByDeletion", func() {
		s.Run("EmptyInputReturnsEmpty", s.issueAffectedByDeletionEmpty)
		s.Run("ReturnsBlockingDependers", s.issueAffectedByDeletionDependers)
	})
	s.Run("RecomputeIsBlocked", func() {
		s.Run("EmptyIDsNoop", s.issueRecomputeIsBlockedEmpty)
		s.Run("FlipsIsBlockedWhenBlockerCloses", s.issueRecomputeIsBlockedFlips)
	})
}

func (s *testSuite) TestDependencySQLRepositoryDelete() {
	s.Run("DeleteAllForIDs", func() {
		s.Run("EmptySliceIsZero", s.depDeleteAllEmpty)
		s.Run("RemovesEdgesTouchingIDs", s.depDeleteAllRemoves)
		s.Run("RoutesToWispDependencies", s.depDeleteAllWispRouting)
	})
	s.Run("CountAllForIDs", func() {
		s.Run("EmptySliceIsZero", s.depCountAllEmpty)
		s.Run("CountsEdgesTouchingIDs", s.depCountAllCounts)
	})
}

func (s *testSuite) TestLabelSQLRepositoryDelete() {
	s.Run("DeleteAllForIDs", func() {
		s.Run("EmptySliceIsZero", s.labelDeleteAllEmpty)
		s.Run("RemovesLabelsForIDs", s.labelDeleteAllRemoves)
		s.Run("RoutesToWispLabels", s.labelDeleteAllWispRouting)
	})
	s.Run("CountAllForIDs", func() {
		s.Run("EmptySliceIsZero", s.labelCountAllEmpty)
		s.Run("CountsLabelsForIDs", s.labelCountAllCounts)
		s.Run("RoutesToWispLabels", s.labelCountAllWispRouting)
	})
}

func (s *testSuite) TestEventsSQLRepositoryDelete() {
	s.Run("DeleteAllForIDs", func() {
		s.Run("EmptySliceIsZero", s.eventsDeleteAllEmpty)
		s.Run("RemovesEventsForIDs", s.eventsDeleteAllRemoves)
		s.Run("RoutesToWispEvents", s.eventsDeleteAllWispRouting)
	})
	s.Run("CountAllForIDs", func() {
		s.Run("EmptySliceIsZero", s.eventsCountAllEmpty)
		s.Run("CountsEventsForIDs", s.eventsCountAllCounts)
		s.Run("RoutesToWispEvents", s.eventsCountAllWispRouting)
	})
}

func (s *testSuite) issueDeleteRemovesRow() {
	s.seedIssueRow("bd-del-r1")
	r := s.issueRepo()
	s.Require().NoError(r.Delete(s.Ctx(), "bd-del-r1", domain.IssueTableOpts{}))

	var c int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM issues WHERE id = ?", "bd-del-r1").Scan(&c))
	s.Equal(0, c)
}

func (s *testSuite) issueDeleteMissing() {
	r := s.issueRepo()
	err := r.Delete(s.Ctx(), "bd-del-nope", domain.IssueTableOpts{})
	s.Require().Error(err)
	s.Contains(err.Error(), "not found")
}

func (s *testSuite) issueDeleteWispRouting() {
	s.seedWispRow("bd-del-w1")
	s.seedIssueRow("bd-del-w1")

	r := s.issueRepo()
	s.Require().NoError(r.Delete(s.Ctx(), "bd-del-w1", domain.IssueTableOpts{UseWispsTable: true}))

	var wispCount, issueCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisps WHERE id = ?", "bd-del-w1").Scan(&wispCount))
	s.Equal(0, wispCount, "wisp row should be deleted")
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM issues WHERE id = ?", "bd-del-w1").Scan(&issueCount))
	s.Equal(1, issueCount, "issues row must be untouched")
}

func (s *testSuite) issueDeleteByIDsEmpty() {
	n, err := s.issueRepo().DeleteByIDs(s.Ctx(), nil, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal(0, n)
}

func (s *testSuite) issueDeleteByIDsBulk() {
	s.seedIssueRow("bd-del-bulk-a")
	s.seedIssueRow("bd-del-bulk-b")
	s.seedIssueRow("bd-del-bulk-c")

	n, err := s.issueRepo().DeleteByIDs(s.Ctx(),
		[]string{"bd-del-bulk-a", "bd-del-bulk-b", "bd-del-bulk-c"}, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal(3, n)

	var c int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM issues WHERE id IN ('bd-del-bulk-a','bd-del-bulk-b','bd-del-bulk-c')").Scan(&c))
	s.Equal(0, c)
}

func (s *testSuite) issueDeleteByIDsWispRouting() {
	s.seedWispRow("bd-del-bulk-w1")
	s.seedWispRow("bd-del-bulk-w2")
	s.seedIssueRow("bd-del-bulk-w1")

	n, err := s.issueRepo().DeleteByIDs(s.Ctx(),
		[]string{"bd-del-bulk-w1", "bd-del-bulk-w2"}, domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal(2, n)

	var wispCount, issueCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisps WHERE id IN ('bd-del-bulk-w1','bd-del-bulk-w2')").Scan(&wispCount))
	s.Equal(0, wispCount)
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM issues WHERE id = ?", "bd-del-bulk-w1").Scan(&issueCount))
	s.Equal(1, issueCount)
}

func (s *testSuite) issueDeleteByIDsMissing() {
	s.seedIssueRow("bd-del-mix-a")
	n, err := s.issueRepo().DeleteByIDs(s.Ctx(),
		[]string{"bd-del-mix-a", "bd-del-mix-missing"}, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal(1, n, "only one row matched")
}

func (s *testSuite) issuePartitionEmpty() {
	wisp, perm, err := s.issueRepo().PartitionWispIDs(s.Ctx(), nil)
	s.Require().NoError(err)
	s.Nil(wisp)
	s.Nil(perm)
}

func (s *testSuite) issuePartitionSplits() {
	s.seedIssueRow("bd-del-part-i1")
	s.seedIssueRow("bd-del-part-i2")
	s.seedWispRow("bd-del-part-w1")

	wisp, perm, err := s.issueRepo().PartitionWispIDs(s.Ctx(),
		[]string{"bd-del-part-i1", "bd-del-part-w1", "bd-del-part-i2", "bd-del-part-unknown"})
	s.Require().NoError(err)
	s.Equal([]string{"bd-del-part-w1"}, wisp)
	s.Equal([]string{"bd-del-part-i1", "bd-del-part-i2", "bd-del-part-unknown"}, perm,
		"unknown IDs partition as permanent (treated as non-wisp by helper)")
}

func (s *testSuite) issueFindAllDependentsEmpty() {
	out, err := s.issueRepo().FindAllDependents(s.Ctx(), nil)
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) issueFindAllDependentsTransitive() {
	s.seedIssueRow("bd-del-cas-root")
	s.seedIssueRow("bd-del-cas-mid")
	s.seedIssueRow("bd-del-cas-leaf")

	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-del-cas-mid", "bd-del-cas-root", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-del-cas-leaf", "bd-del-cas-mid", types.DepParentChild), "tester", domain.DepInsertOpts{}))

	out, err := s.issueRepo().FindAllDependents(s.Ctx(), []string{"bd-del-cas-root"})
	s.Require().NoError(err)
	got := make(map[string]bool, len(out))
	for _, id := range out {
		got[id] = true
	}
	s.True(got["bd-del-cas-root"], "root included")
	s.True(got["bd-del-cas-mid"], "direct dependent via blocks included")
	s.True(got["bd-del-cas-leaf"], "transitive dependent via parent-child included")
}

func (s *testSuite) issueFindAllDependentsIncludesRoots() {
	s.seedIssueRow("bd-del-cas-solo")
	out, err := s.issueRepo().FindAllDependents(s.Ctx(), []string{"bd-del-cas-solo"})
	s.Require().NoError(err)
	s.Equal([]string{"bd-del-cas-solo"}, out)
}

func (s *testSuite) issueAffectedByDeletionEmpty() {
	issues, wisps, err := s.issueRepo().AffectedByDeletion(s.Ctx(), nil, nil)
	s.Require().NoError(err)
	s.Empty(issues)
	s.Empty(wisps)
}

func (s *testSuite) issueAffectedByDeletionDependers() {
	s.seedIssueRow("bd-del-aff-blocker")
	s.seedIssueRow("bd-del-aff-depender")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-del-aff-depender", "bd-del-aff-blocker", types.DepBlocks),
		"tester", domain.DepInsertOpts{}))

	issues, _, err := s.issueRepo().AffectedByDeletion(s.Ctx(),
		[]string{"bd-del-aff-blocker"}, nil)
	s.Require().NoError(err)
	got := make(map[string]bool, len(issues))
	for _, id := range issues {
		got[id] = true
	}
	s.True(got["bd-del-aff-depender"], "depender must appear in affected set")
}

func (s *testSuite) issueRecomputeIsBlockedEmpty() {
	s.Require().NoError(s.issueRepo().RecomputeIsBlocked(s.Ctx(), nil, nil))
}

func (s *testSuite) issueRecomputeIsBlockedFlips() {
	s.seedIssueRow("bd-del-rib-blocker")
	s.seedIssueRow("bd-del-rib-depender")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-del-rib-depender", "bd-del-rib-blocker", types.DepBlocks),
		"tester", domain.DepInsertOpts{}))

	s.Require().NoError(s.issueRepo().RecomputeIsBlocked(s.Ctx(),
		[]string{"bd-del-rib-depender"}, nil))
	var blocked int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT is_blocked FROM issues WHERE id = ?", "bd-del-rib-depender").Scan(&blocked))
	s.Equal(1, blocked, "depender should be blocked while blocker is open")

	_, err := s.Runner().ExecContext(s.Ctx(),
		"UPDATE issues SET status = 'closed' WHERE id = ?", "bd-del-rib-blocker")
	s.Require().NoError(err)
	s.Require().NoError(s.issueRepo().RecomputeIsBlocked(s.Ctx(),
		[]string{"bd-del-rib-depender"}, nil))
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT is_blocked FROM issues WHERE id = ?", "bd-del-rib-depender").Scan(&blocked))
	s.Equal(0, blocked, "depender should unblock once blocker is closed")
}

func (s *testSuite) depDeleteAllEmpty() {
	n, err := s.depRepo().DeleteAllForIDs(s.Ctx(), nil, domain.DepInsertOpts{})
	s.Require().NoError(err)
	s.Equal(0, n)
}

func (s *testSuite) depDeleteAllRemoves() {
	s.seedIssueRow("bd-del-da-a")
	s.seedIssueRow("bd-del-da-b")
	s.seedIssueRow("bd-del-da-c")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-del-da-a", "bd-del-da-c", types.DepBlocks),
		"tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-del-da-b", "bd-del-da-a", types.DepBlocks),
		"tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-del-da-b", "bd-del-da-c", types.DepBlocks),
		"tester", domain.DepInsertOpts{}))

	n, err := r.DeleteAllForIDs(s.Ctx(), []string{"bd-del-da-a"}, domain.DepInsertOpts{})
	s.Require().NoError(err)
	s.Equal(2, n)

	var remaining int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM dependencies WHERE issue_id IN ('bd-del-da-a','bd-del-da-b','bd-del-da-c')").Scan(&remaining))
	s.Equal(1, remaining, "only B->C should remain")
}

func (s *testSuite) depDeleteAllWispRouting() {
	s.seedWispRow("bd-del-dw-src")
	s.seedIssueRow("bd-del-dw-tgt")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(),
		newDep("bd-del-dw-src", "bd-del-dw-tgt", types.DepBlocks), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))

	n, err := r.DeleteAllForIDs(s.Ctx(), []string{"bd-del-dw-src"},
		domain.DepInsertOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal(1, n)

	var wispCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?", "bd-del-dw-src").Scan(&wispCount))
	s.Equal(0, wispCount)
}

func (s *testSuite) depCountAllEmpty() {
	n, err := s.depRepo().CountAllForIDs(s.Ctx(), nil, domain.DepCountsOpts{})
	s.Require().NoError(err)
	s.Equal(0, n)
}

func (s *testSuite) depCountAllCounts() {
	s.seedIssueRow("bd-del-cnt-a")
	s.seedIssueRow("bd-del-cnt-b")
	s.seedIssueRow("bd-del-cnt-c")
	r := s.depRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-del-cnt-a", "bd-del-cnt-c", types.DepBlocks),
		"tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-del-cnt-b", "bd-del-cnt-a", types.DepBlocks),
		"tester", domain.DepInsertOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), newDep("bd-del-cnt-b", "bd-del-cnt-c", types.DepBlocks),
		"tester", domain.DepInsertOpts{}))

	n, err := r.CountAllForIDs(s.Ctx(), []string{"bd-del-cnt-a"}, domain.DepCountsOpts{})
	s.Require().NoError(err)
	s.Equal(2, n, "edges touching A on either end")
}

func (s *testSuite) labelDeleteAllEmpty() {
	n, err := s.labelRepo().DeleteAllForIDs(s.Ctx(), nil, domain.LabelOpts{})
	s.Require().NoError(err)
	s.Equal(0, n)
}

func (s *testSuite) labelDeleteAllRemoves() {
	s.seedIssueRow("bd-del-lbl-a")
	s.seedIssueRow("bd-del-lbl-b")
	s.seedIssueRow("bd-del-lbl-c")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-del-lbl-a", "x", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-del-lbl-a", "y", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-del-lbl-b", "z", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-del-lbl-c", "keep", "tester", domain.LabelOpts{}))

	n, err := r.DeleteAllForIDs(s.Ctx(),
		[]string{"bd-del-lbl-a", "bd-del-lbl-b"}, domain.LabelOpts{})
	s.Require().NoError(err)
	s.Equal(3, n)

	var remaining int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM labels WHERE issue_id IN ('bd-del-lbl-a','bd-del-lbl-b','bd-del-lbl-c')").Scan(&remaining))
	s.Equal(1, remaining, "only the untouched issue's label survives")
}

func (s *testSuite) labelDeleteAllWispRouting() {
	s.seedWispRow("bd-del-wlbl-1")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-del-wlbl-1", "alpha", "tester",
		domain.LabelOpts{UseWispsTable: true}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-del-wlbl-1", "beta", "tester",
		domain.LabelOpts{UseWispsTable: true}))

	n, err := r.DeleteAllForIDs(s.Ctx(), []string{"bd-del-wlbl-1"},
		domain.LabelOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal(2, n)

	var wispCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?", "bd-del-wlbl-1").Scan(&wispCount))
	s.Equal(0, wispCount)
}

func (s *testSuite) labelCountAllEmpty() {
	n, err := s.labelRepo().CountAllForIDs(s.Ctx(), nil, domain.LabelOpts{})
	s.Require().NoError(err)
	s.Equal(0, n)
}

func (s *testSuite) labelCountAllCounts() {
	s.seedIssueRow("bd-del-lcnt-a")
	s.seedIssueRow("bd-del-lcnt-b")
	s.seedIssueRow("bd-del-lcnt-c")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-del-lcnt-a", "x", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-del-lcnt-a", "y", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-del-lcnt-b", "z", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-del-lcnt-c", "ignored", "tester", domain.LabelOpts{}))

	n, err := r.CountAllForIDs(s.Ctx(),
		[]string{"bd-del-lcnt-a", "bd-del-lcnt-b"}, domain.LabelOpts{})
	s.Require().NoError(err)
	s.Equal(3, n)
}

func (s *testSuite) labelCountAllWispRouting() {
	s.seedWispRow("bd-del-wlcnt-1")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-del-wlcnt-1", "alpha", "tester",
		domain.LabelOpts{UseWispsTable: true}))

	n, err := r.CountAllForIDs(s.Ctx(), []string{"bd-del-wlcnt-1"},
		domain.LabelOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal(1, n, "wisp-routed count must read from wisp_labels")
}

func (s *testSuite) eventsDeleteAllEmpty() {
	n, err := s.eventsRepo().DeleteAllForIDs(s.Ctx(), nil, domain.RecordEventOpts{})
	s.Require().NoError(err)
	s.Equal(0, n)
}

func (s *testSuite) eventsDeleteAllRemoves() {
	s.seedIssueRow("bd-del-evt-a")
	s.seedIssueRow("bd-del-evt-b")
	s.seedIssueRow("bd-del-evt-c")
	r := s.eventsRepo()
	for _, id := range []string{"bd-del-evt-a", "bd-del-evt-a", "bd-del-evt-b", "bd-del-evt-c"} {
		s.Require().NoError(r.Record(s.Ctx(),
			domain.Event{IssueID: id, Type: types.EventCreated, Actor: "tester"},
			domain.RecordEventOpts{}))
	}

	n, err := r.DeleteAllForIDs(s.Ctx(),
		[]string{"bd-del-evt-a", "bd-del-evt-b"}, domain.RecordEventOpts{})
	s.Require().NoError(err)
	s.Equal(3, n)

	var remaining int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id IN ('bd-del-evt-a','bd-del-evt-b','bd-del-evt-c')").Scan(&remaining))
	s.Equal(1, remaining)
}

func (s *testSuite) eventsDeleteAllWispRouting() {
	s.seedWispRow("bd-del-wevt-1")
	r := s.eventsRepo()
	s.Require().NoError(r.Record(s.Ctx(),
		domain.Event{IssueID: "bd-del-wevt-1", Type: types.EventUpdated, Actor: "tester"},
		domain.RecordEventOpts{UseWispsTable: true}))

	n, err := r.DeleteAllForIDs(s.Ctx(), []string{"bd-del-wevt-1"},
		domain.RecordEventOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal(1, n)
}

func (s *testSuite) eventsCountAllEmpty() {
	n, err := s.eventsRepo().CountAllForIDs(s.Ctx(), nil, domain.RecordEventOpts{})
	s.Require().NoError(err)
	s.Equal(0, n)
}

func (s *testSuite) eventsCountAllCounts() {
	s.seedIssueRow("bd-del-ec-a")
	s.seedIssueRow("bd-del-ec-b")
	r := s.eventsRepo()
	for _, id := range []string{"bd-del-ec-a", "bd-del-ec-a", "bd-del-ec-b"} {
		s.Require().NoError(r.Record(s.Ctx(),
			domain.Event{IssueID: id, Type: types.EventCreated, Actor: "tester"},
			domain.RecordEventOpts{}))
	}

	n, err := r.CountAllForIDs(s.Ctx(), []string{"bd-del-ec-a", "bd-del-ec-b"}, domain.RecordEventOpts{})
	s.Require().NoError(err)
	s.Equal(3, n)
}

func (s *testSuite) eventsCountAllWispRouting() {
	s.seedWispRow("bd-del-wec-1")
	r := s.eventsRepo()
	s.Require().NoError(r.Record(s.Ctx(),
		domain.Event{IssueID: "bd-del-wec-1", Type: types.EventUpdated, Actor: "tester"},
		domain.RecordEventOpts{UseWispsTable: true}))

	n, err := r.CountAllForIDs(s.Ctx(), []string{"bd-del-wec-1"},
		domain.RecordEventOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal(1, n, "wisp-routed count must read from wisp_events")
}
