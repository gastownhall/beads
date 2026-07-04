package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestLabelSQLRepository() {
	s.Run("Insert", func() {
		s.Run("RoundTripWithList", s.labelInsertRoundTrip)
		s.Run("IdempotentDuplicate", s.labelInsertIdempotent)
		s.Run("RecordsLabelAddedEvent", s.labelInsertRecordsEvent)
		s.Run("RejectsEmptyIssueID", s.labelInsertEmptyIssueID)
		s.Run("RejectsEmptyLabel", s.labelInsertEmptyLabel)
		s.Run("MissingIssueIDFailsFK", s.labelInsertFKViolation)
	})
	s.Run("Delete", func() {
		s.Run("RemovesLabelRow", s.labelDeleteRemoves)
		s.Run("RecordsLabelRemovedEvent", s.labelDeleteRecordsEvent)
		s.Run("MissingLabelIsNoop", s.labelDeleteMissingNoop)
		s.Run("OnlyTargetLabelRemoved", s.labelDeleteSpecificLabel)
		s.Run("RejectsEmptyIssueID", s.labelDeleteEmptyIssueID)
		s.Run("RejectsEmptyLabel", s.labelDeleteEmptyLabel)
		s.Run("WispRoutesToWispLabels", s.labelDeleteWispRouting)
	})
	s.Run("List", func() {
		s.Run("OrdersByLabelAlpha", s.labelListAlphaOrder)
		s.Run("UnknownIssueReturnsEmpty", s.labelListUnknown)
	})
	s.Run("ListByIssueIDs", func() {
		s.Run("EmptySliceReturnsEmptyMap", s.labelBulkEmpty)
		s.Run("MultipleIssuesGroupedByID", s.labelBulkGrouped)
		s.Run("MissingIDsAreAbsent", s.labelBulkMissingAbsent)
	})
	s.Run("Wisp", func() {
		s.Run("InsertRoutesToWispLabels", s.labelWispInsertRouting)
		s.Run("InsertRecordsEventInWispEvents", s.labelWispInsertEvent)
		s.Run("ListReadsFromWispLabels", s.labelWispListIsolated)
		s.Run("ListByIssueIDsReadsFromWispLabels", s.labelWispBulkIsolated)
	})
}

func (s *testSuite) labelRepo() domain.LabelSQLRepository {
	return NewLabelSQLRepository(s.Runner())
}

func (s *testSuite) labelInsertRoundTrip() {
	s.seedIssueRow("bd-lbl-1")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-1", "tech-debt", "tester", domain.LabelOpts{}))

	out, err := r.List(s.Ctx(), "bd-lbl-1", domain.LabelOpts{})
	s.Require().NoError(err)
	s.Equal([]string{"tech-debt"}, out)
}

func (s *testSuite) labelInsertIdempotent() {
	s.seedIssueRow("bd-lbl-dup")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-dup", "needs-review", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-dup", "needs-review", "tester", domain.LabelOpts{}))

	out, err := r.List(s.Ctx(), "bd-lbl-dup", domain.LabelOpts{})
	s.Require().NoError(err)
	s.Equal([]string{"needs-review"}, out, "duplicate label add should be a no-op on the labels table")

	var count int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-lbl-dup", string(types.EventLabelAdded),
	).Scan(&count))
	s.Equal(2, count)
}

func (s *testSuite) labelInsertRecordsEvent() {
	s.seedIssueRow("bd-lbl-evt")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-evt", "perf", "alice", domain.LabelOpts{}))

	var actor, newValue string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT actor, new_value FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-lbl-evt", string(types.EventLabelAdded),
	).Scan(&actor, &newValue))
	s.Equal("alice", actor)
	s.Equal("perf", newValue, "event new_value should carry the label name")
}

func (s *testSuite) labelInsertEmptyIssueID() {
	err := s.labelRepo().Insert(s.Ctx(), "", "x", "tester", domain.LabelOpts{})
	s.Require().Error(err)
}

func (s *testSuite) labelInsertEmptyLabel() {
	err := s.labelRepo().Insert(s.Ctx(), "bd-lbl-x", "", "tester", domain.LabelOpts{})
	s.Require().Error(err)
}

func (s *testSuite) labelInsertFKViolation() {
	err := s.labelRepo().Insert(s.Ctx(), "bd-no-such-issue", "x", "tester", domain.LabelOpts{})
	s.Require().Error(err, "expected FK violation when issue_id does not exist")
}

func (s *testSuite) labelListAlphaOrder() {
	s.seedIssueRow("bd-lbl-ord")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-ord", "zeta", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-ord", "alpha", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-ord", "mu", "tester", domain.LabelOpts{}))

	out, err := r.List(s.Ctx(), "bd-lbl-ord", domain.LabelOpts{})
	s.Require().NoError(err)
	s.Equal([]string{"alpha", "mu", "zeta"}, out)
}

func (s *testSuite) labelListUnknown() {
	out, err := s.labelRepo().List(s.Ctx(), "bd-no-labels-here", domain.LabelOpts{})
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) labelBulkEmpty() {
	out, err := s.labelRepo().ListByIssueIDs(s.Ctx(), nil, domain.LabelOpts{})
	s.Require().NoError(err)
	s.NotNil(out, "ListByIssueIDs should return a non-nil empty map")
	s.Empty(out)
}

func (s *testSuite) labelBulkGrouped() {
	s.seedIssueRow("bd-lbl-bulk-1")
	s.seedIssueRow("bd-lbl-bulk-2")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-bulk-1", "a", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-bulk-1", "b", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-bulk-2", "c", "tester", domain.LabelOpts{}))

	out, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-lbl-bulk-1", "bd-lbl-bulk-2"}, domain.LabelOpts{})
	s.Require().NoError(err)
	s.Equal([]string{"a", "b"}, out["bd-lbl-bulk-1"])
	s.Equal([]string{"c"}, out["bd-lbl-bulk-2"])
}

func (s *testSuite) labelBulkMissingAbsent() {
	s.seedIssueRow("bd-lbl-present")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-present", "x", "tester", domain.LabelOpts{}))

	out, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-lbl-present", "bd-lbl-missing"}, domain.LabelOpts{})
	s.Require().NoError(err)
	s.Equal([]string{"x"}, out["bd-lbl-present"])
	_, present := out["bd-lbl-missing"]
	s.False(present, "missing issue IDs should not appear in the result map")
}

func (s *testSuite) labelWispInsertRouting() {
	s.seedWispRow("bd-lbl-wisp-1")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-wisp-1", "wisp-only", "tester", domain.LabelOpts{UseWispsTable: true}))

	var wispCount, permCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?", "bd-lbl-wisp-1").Scan(&wispCount))
	s.Equal(1, wispCount)
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM labels WHERE issue_id = ?", "bd-lbl-wisp-1").Scan(&permCount))
	s.Equal(0, permCount, "wisp-routed insert must not write to labels")
}

func (s *testSuite) labelWispInsertEvent() {
	s.seedWispRow("bd-lbl-wisp-evt")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-wisp-evt", "audit", "alice", domain.LabelOpts{UseWispsTable: true}))

	var wispEvtCount, permEvtCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ? AND event_type = ?",
		"bd-lbl-wisp-evt", string(types.EventLabelAdded),
	).Scan(&wispEvtCount))
	s.Equal(1, wispEvtCount)
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-lbl-wisp-evt", string(types.EventLabelAdded),
	).Scan(&permEvtCount))
	s.Equal(0, permEvtCount, "wisp-routed label event must not write to events")
}

func (s *testSuite) labelWispListIsolated() {
	// Same issue ID in both tables (won't happen in practice, but proves the routing
	// is strict — List with UseWispsTable=true sees only wisp_labels rows).
	s.seedIssueRow("bd-lbl-iso-perm")
	s.seedWispRow("bd-lbl-iso-wisp")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-iso-perm", "perm", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-iso-wisp", "wisp", "tester", domain.LabelOpts{UseWispsTable: true}))

	permLabels, err := r.List(s.Ctx(), "bd-lbl-iso-perm", domain.LabelOpts{})
	s.Require().NoError(err)
	s.Equal([]string{"perm"}, permLabels)

	wispLabels, err := r.List(s.Ctx(), "bd-lbl-iso-wisp", domain.LabelOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal([]string{"wisp"}, wispLabels)

	// Cross-routed lookups should be empty.
	empty, err := r.List(s.Ctx(), "bd-lbl-iso-wisp", domain.LabelOpts{})
	s.Require().NoError(err)
	s.Empty(empty)
	empty, err = r.List(s.Ctx(), "bd-lbl-iso-perm", domain.LabelOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Empty(empty)
}

func (s *testSuite) labelWispBulkIsolated() {
	s.seedWispRow("bd-lbl-wbulk-1")
	s.seedWispRow("bd-lbl-wbulk-2")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-wbulk-1", "a", "tester", domain.LabelOpts{UseWispsTable: true}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-wbulk-1", "b", "tester", domain.LabelOpts{UseWispsTable: true}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-wbulk-2", "c", "tester", domain.LabelOpts{UseWispsTable: true}))

	out, err := r.ListByIssueIDs(s.Ctx(), []string{"bd-lbl-wbulk-1", "bd-lbl-wbulk-2"}, domain.LabelOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal([]string{"a", "b"}, out["bd-lbl-wbulk-1"])
	s.Equal([]string{"c"}, out["bd-lbl-wbulk-2"])
}

func (s *testSuite) labelDeleteRemoves() {
	s.seedIssueRow("bd-lbl-del-1")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-del-1", "tech-debt", "tester", domain.LabelOpts{}))

	s.Require().NoError(r.Delete(s.Ctx(), "bd-lbl-del-1", "tech-debt", "tester", domain.LabelOpts{}))

	out, err := r.List(s.Ctx(), "bd-lbl-del-1", domain.LabelOpts{})
	s.Require().NoError(err)
	s.Empty(out, "deleted label should no longer appear in List")
}

func (s *testSuite) labelDeleteRecordsEvent() {
	s.seedIssueRow("bd-lbl-del-evt")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-del-evt", "perf", "alice", domain.LabelOpts{}))
	s.Require().NoError(r.Delete(s.Ctx(), "bd-lbl-del-evt", "perf", "bob", domain.LabelOpts{}))

	var actor, oldValue string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT actor, old_value FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-lbl-del-evt", string(types.EventLabelRemoved),
	).Scan(&actor, &oldValue))
	s.Equal("bob", actor)
	s.Equal("perf", oldValue, "event old_value should carry the removed label name (symmetric with Insert's new_value)")
}

func (s *testSuite) labelDeleteMissingNoop() {
	s.seedIssueRow("bd-lbl-del-miss")
	r := s.labelRepo()
	s.Require().NoError(r.Delete(s.Ctx(), "bd-lbl-del-miss", "never-there", "tester", domain.LabelOpts{}))

	out, err := r.List(s.Ctx(), "bd-lbl-del-miss", domain.LabelOpts{})
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) labelDeleteSpecificLabel() {
	s.seedIssueRow("bd-lbl-del-specific")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-del-specific", "keep", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-del-specific", "drop", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-del-specific", "stay", "tester", domain.LabelOpts{}))

	s.Require().NoError(r.Delete(s.Ctx(), "bd-lbl-del-specific", "drop", "tester", domain.LabelOpts{}))

	out, err := r.List(s.Ctx(), "bd-lbl-del-specific", domain.LabelOpts{})
	s.Require().NoError(err)
	s.Equal([]string{"keep", "stay"}, out, "Delete must target only the named label, not siblings on the same issue")
}

func (s *testSuite) labelDeleteEmptyIssueID() {
	err := s.labelRepo().Delete(s.Ctx(), "", "x", "tester", domain.LabelOpts{})
	s.Require().Error(err)
}

func (s *testSuite) labelDeleteEmptyLabel() {
	err := s.labelRepo().Delete(s.Ctx(), "bd-lbl-del-x", "", "tester", domain.LabelOpts{})
	s.Require().Error(err)
}

func (s *testSuite) labelDeleteWispRouting() {
	s.seedIssueRow("bd-lbl-del-cross-perm")
	s.seedWispRow("bd-lbl-del-cross-wisp")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-del-cross-perm", "shared", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lbl-del-cross-wisp", "shared", "tester", domain.LabelOpts{UseWispsTable: true}))

	s.Require().NoError(r.Delete(s.Ctx(), "bd-lbl-del-cross-wisp", "shared", "tester", domain.LabelOpts{UseWispsTable: true}))

	var wispCount, permCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?", "bd-lbl-del-cross-wisp").Scan(&wispCount))
	s.Equal(0, wispCount, "wisp-routed Delete must remove the wisp_labels row")
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM labels WHERE issue_id = ?", "bd-lbl-del-cross-perm").Scan(&permCount))
	s.Equal(1, permCount, "wisp-routed Delete must not touch the labels table")

	var wispEvt, permEvt int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ? AND event_type = ?",
		"bd-lbl-del-cross-wisp", string(types.EventLabelRemoved),
	).Scan(&wispEvt))
	s.Equal(1, wispEvt)
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-lbl-del-cross-wisp", string(types.EventLabelRemoved),
	).Scan(&permEvt))
	s.Equal(0, permEvt, "wisp-routed Delete must record the event in wisp_events, not events")
}
