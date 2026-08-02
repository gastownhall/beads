package db

import (
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueSQLRepositoryReopen() {
	s.Run("ReopensClosedIssue", s.reopenMutatesRow)
	s.Run("IdempotentOnAlreadyOpen", s.reopenIdempotent)
	s.Run("RecomputesIsBlockedOnDependents", s.reopenRecomputesIsBlocked)
	s.Run("MissingIDErrors", s.reopenMissingID)
	s.Run("RoutesWisp", s.reopenRoutesWisp)
	s.Run("AppendsCommentOnReason", s.reopenAppendsComment)
	s.Run("NoCommentOnEmptyReason", s.reopenNoCommentEmptyReason)
	s.Run("ReopensCustomDonePermanentAndClearsLifecycleFields", s.reopenCustomDonePermanent)
	s.Run("ReopensCustomDoneWisp", s.reopenCustomDoneWisp)
	s.Run("LeavesCustomNonDoneAndUnspecifiedStatusesUntouched", s.reopenLeavesIneligibleStatusesUntouched)
	s.Run("ConfigurationFailureRollsBack", s.reopenConfigurationFailureRollsBack)
}

func (s *testSuite) reopenMutatesRow() {
	s.seedIssueRow("bd-ro-row")
	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-ro-row",
		domain.CloseRowParams{Reason: "done", Session: "sess-1"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	_, err = s.Runner().ExecContext(s.Ctx(), `
		INSERT INTO leases (issue_id, holder, granted_at, lease_expires_at, heartbeat_at)
		VALUES (?, ?, UTC_TIMESTAMP(), DATE_ADD(UTC_TIMESTAMP(), INTERVAL 5 MINUTE), UTC_TIMESTAMP())
	`, "bd-ro-row", "stale-worker")
	s.Require().NoError(err)

	res, err := r.Reopen(s.Ctx(), "bd-ro-row",
		domain.ReopenRowParams{Reason: "not really done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.True(res.Updated)
	s.False(res.AlreadyOpen)
	s.False(res.IsWisp)

	var status, reason, session string
	var closedAt *string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT status, closed_at, close_reason, closed_by_session FROM issues WHERE id = ?", "bd-ro-row").
		Scan(&status, &closedAt, &reason, &session))
	s.Equal(string(types.StatusOpen), status)
	s.Nil(closedAt, "closed_at must be cleared on reopen")
	s.Equal("", reason)
	s.Equal("", session)

	var evtCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-ro-row", string(types.EventReopened)).Scan(&evtCount))
	s.Equal(1, evtCount)

	var leaseCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM leases WHERE issue_id = ?", "bd-ro-row").Scan(&leaseCount))
	s.Equal(0, leaseCount, "reopen must remove a seeded lease")
}

func (s *testSuite) reopenIdempotent() {
	s.seedIssueRow("bd-ro-idem")
	r := NewIssueSQLRepository(s.Runner())

	res, err := r.Reopen(s.Ctx(), "bd-ro-idem",
		domain.ReopenRowParams{}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.False(res.Updated)
	s.True(res.AlreadyOpen)

	var evtCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-ro-idem", string(types.EventReopened)).Scan(&evtCount))
	s.Equal(0, evtCount, "must not record event when nothing changed")
}

func (s *testSuite) reopenRecomputesIsBlocked() {
	s.seedIssueRow("bd-ro-src")
	s.seedIssueRow("bd-ro-tgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-ro-src", "bd-ro-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-ro-tgt",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.False(s.isBlocked("bd-ro-src"))

	_, err = r.Reopen(s.Ctx(), "bd-ro-tgt",
		domain.ReopenRowParams{}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.True(s.isBlocked("bd-ro-src"), "reopening the blocker must re-block the dependent")
}

func (s *testSuite) reopenMissingID() {
	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Reopen(s.Ctx(), "bd-ro-missing",
		domain.ReopenRowParams{}, "tester", domain.IssueTableOpts{})
	s.Require().Error(err)
	s.ErrorIs(err, storage.ErrNotFound)
}

func (s *testSuite) reopenRoutesWisp() {
	s.seedWispRow("bd-ro-wisp")
	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-ro-wisp",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)

	res, err := r.Reopen(s.Ctx(), "bd-ro-wisp",
		domain.ReopenRowParams{}, "tester", domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.True(res.Updated)
	s.True(res.IsWisp)

	var status string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT status FROM wisps WHERE id = ?", "bd-ro-wisp").Scan(&status))
	s.Equal(string(types.StatusOpen), status)

	var evtCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ? AND event_type = ?",
		"bd-ro-wisp", string(types.EventReopened)).Scan(&evtCount))
	s.Equal(1, evtCount)
}

func (s *testSuite) reopenAppendsComment() {
	s.seedIssueRow("bd-ro-cmt")
	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-ro-cmt",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	_, err = r.Reopen(s.Ctx(), "bd-ro-cmt",
		domain.ReopenRowParams{Reason: "regression spotted"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	var comment string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT comment FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-ro-cmt", string(types.EventCommented)).Scan(&comment))
	s.Equal("regression spotted", comment)
}

func (s *testSuite) reopenNoCommentEmptyReason() {
	s.seedIssueRow("bd-ro-nocmt")
	r := NewIssueSQLRepository(s.Runner())
	_, err := r.Close(s.Ctx(), "bd-ro-nocmt",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	_, err = r.Reopen(s.Ctx(), "bd-ro-nocmt",
		domain.ReopenRowParams{}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)

	var cmtCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-ro-nocmt", string(types.EventCommented)).Scan(&cmtCount))
	s.Equal(0, cmtCount)
}

func (s *testSuite) reopenCustomDonePermanent() {
	const customStatus = "archived-permanent"

	s.seedIssueRow("bd-ro-custom-done")
	_, err := s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO custom_statuses (name, category) VALUES (?, ?)", customStatus, string(types.CategoryDone))
	s.Require().NoError(err)
	_, err = s.Runner().ExecContext(s.Ctx(), `
		UPDATE issues
		SET status = ?, closed_at = UTC_TIMESTAMP(), close_reason = ?, closed_by_session = ?,
			defer_until = DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY)
		WHERE id = ?
	`, customStatus, "completed", "session-1", "bd-ro-custom-done")
	s.Require().NoError(err)

	res, err := NewIssueSQLRepository(s.Runner()).Reopen(s.Ctx(), "bd-ro-custom-done",
		domain.ReopenRowParams{Reason: "needs another pass"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.True(res.Updated)

	var status, closeReason, closedBySession string
	var closedAt, deferUntil sql.NullTime
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(), `
		SELECT status, closed_at, close_reason, closed_by_session, defer_until
		FROM issues WHERE id = ?
	`, "bd-ro-custom-done").Scan(&status, &closedAt, &closeReason, &closedBySession, &deferUntil))
	s.Equal(string(types.StatusOpen), status)
	s.False(closedAt.Valid, "closed_at must be cleared")
	s.Equal("", closeReason)
	s.Equal("", closedBySession)
	s.False(deferUntil.Valid, "defer_until must be cleared")

	var reopened, comments int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", "bd-ro-custom-done", string(types.EventReopened)).Scan(&reopened))
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", "bd-ro-custom-done", string(types.EventCommented)).Scan(&comments))
	s.Equal(1, reopened)
	s.Equal(1, comments)
}

func (s *testSuite) reopenCustomDoneWisp() {
	const customStatus = "archived-wisp"

	s.seedWispRow("bd-ro-custom-done-wisp")
	_, err := s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO custom_statuses (name, category) VALUES (?, ?)", customStatus, string(types.CategoryDone))
	s.Require().NoError(err)
	_, err = s.Runner().ExecContext(s.Ctx(), `
		UPDATE wisps
		SET status = ?, closed_at = UTC_TIMESTAMP(), close_reason = ?, closed_by_session = ?,
			defer_until = DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY)
		WHERE id = ?
	`, customStatus, "completed", "session-1", "bd-ro-custom-done-wisp")
	s.Require().NoError(err)

	res, err := NewIssueSQLRepository(s.Runner()).Reopen(s.Ctx(), "bd-ro-custom-done-wisp",
		domain.ReopenRowParams{}, "tester", domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.True(res.Updated)
	s.True(res.IsWisp)

	var status, closeReason, closedBySession string
	var closedAt, deferUntil sql.NullTime
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(), `
		SELECT status, closed_at, close_reason, closed_by_session, defer_until
		FROM wisps WHERE id = ?
	`, "bd-ro-custom-done-wisp").Scan(&status, &closedAt, &closeReason, &closedBySession, &deferUntil))
	s.Equal(string(types.StatusOpen), status)
	s.False(closedAt.Valid, "closed_at must be cleared")
	s.Equal("", closeReason)
	s.Equal("", closedBySession)
	s.False(deferUntil.Valid, "defer_until must be cleared")
}

func (s *testSuite) reopenLeavesIneligibleStatusesUntouched() {
	r := NewIssueSQLRepository(s.Runner())
	statuses := []struct {
		id       string
		status   string
		category types.StatusCategory
	}{
		{id: "bd-ro-custom-active", status: "triaged", category: types.CategoryActive},
		{id: "bd-ro-custom-wip", status: "testing", category: types.CategoryWIP},
		{id: "bd-ro-custom-frozen", status: "on-ice", category: types.CategoryFrozen},
		{id: "bd-ro-custom-unknown", status: "queued", category: types.CategoryUnspecified},
	}
	var err error
	for _, status := range statuses[:3] {
		s.seedIssueRow(status.id)
		_, err = s.Runner().ExecContext(s.Ctx(),
			"INSERT INTO custom_statuses (name, category) VALUES (?, ?)", status.status, string(status.category))
		s.Require().NoError(err)
		_, err = s.Runner().ExecContext(s.Ctx(),
			"UPDATE issues SET status = ?, close_reason = ? WHERE id = ?", status.status, "keep", status.id)
		s.Require().NoError(err)
	}
	s.seedIssueRow(statuses[3].id)
	_, err = s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO config (`key`, value) VALUES (?, ?)", "status.custom", "queued")
	s.Require().NoError(err)
	_, err = s.Runner().ExecContext(s.Ctx(),
		"UPDATE issues SET status = ?, close_reason = ? WHERE id = ?", "queued", "keep", statuses[3].id)
	s.Require().NoError(err)

	for _, want := range statuses {
		res, err := r.Reopen(s.Ctx(), want.id, domain.ReopenRowParams{Reason: "ignored"}, "tester", domain.IssueTableOpts{})
		s.Require().NoError(err)
		s.False(res.Updated, "%s should not reopen", want.id)
		s.False(res.AlreadyOpen, "%s is not literally open", want.id)

		var status, closeReason string
		var events int
		s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
			"SELECT status, close_reason FROM issues WHERE id = ?", want.id).Scan(&status, &closeReason))
		s.Equal(want.status, status)
		s.Equal("keep", closeReason)
		s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
			"SELECT COUNT(*) FROM events WHERE issue_id = ?", want.id).Scan(&events))
		s.Equal(0, events, "%s must not emit reopen or comment events", want.id)
	}
}

func (s *testSuite) reopenConfigurationFailureRollsBack() {
	const customStatus = "missing-config-status"

	s.seedIssueRow("bd-ro-config-failure")
	_, err := NewIssueSQLRepository(s.Runner()).Close(s.Ctx(), "bd-ro-config-failure",
		domain.CloseRowParams{Reason: "done"}, "tester", domain.IssueTableOpts{})
	s.Require().NoError(err)
	_, err = s.Runner().ExecContext(s.Ctx(),
		"UPDATE issues SET status = ? WHERE id = ?", customStatus, "bd-ro-config-failure")
	s.Require().NoError(err)
	_, err = s.Runner().ExecContext(s.Ctx(), "DELETE FROM custom_statuses")
	s.Require().NoError(err)
	_, err = s.Runner().ExecContext(s.Ctx(), "DROP TABLE config")
	s.Require().NoError(err)

	_, err = NewIssueSQLRepository(s.Runner()).Reopen(s.Ctx(), "bd-ro-config-failure",
		domain.ReopenRowParams{Reason: "must roll back"}, "tester", domain.IssueTableOpts{})
	s.Require().Error(err)

	var status string
	var reopened, comments int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT status FROM issues WHERE id = ?", "bd-ro-config-failure").Scan(&status))
	s.Equal(customStatus, status)
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", "bd-ro-config-failure", string(types.EventReopened)).Scan(&reopened))
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", "bd-ro-config-failure", string(types.EventCommented)).Scan(&comments))
	s.Equal(0, reopened)
	s.Equal(0, comments)
}
