package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) issueUpdateSameStatusInUnitOfWork() {
	ctx := s.Ctx()
	issue := newTestIssue("same-status-uow", "before")
	s.Require().NoError(s.issueRepo().Insert(ctx, issue, "tester", domain.InsertIssueOpts{}))
	var eventsBefore int
	s.Require().NoError(s.Runner().QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", issue.ID).Scan(&eventsBefore))

	tx, err := s.db.BeginTx(ctx, nil)
	s.Require().NoError(err)
	repo := NewIssueSQLRepository(tx)
	before, err := repo.Get(ctx, issue.ID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Require().NoError(repo.Update(ctx, issue.ID, map[string]any{"status": types.StatusOpen}, "tester", domain.IssueTableOpts{}))
	afterNoop, err := repo.Get(ctx, issue.ID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal(before.RowVersion, afterNoop.RowVersion)
	var eventsAfterNoop, statusChangedAfterNoop int
	s.Require().NoError(tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", issue.ID).Scan(&eventsAfterNoop))
	s.Require().NoError(tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", issue.ID, string(types.EventStatusChanged)).Scan(&statusChangedAfterNoop))
	s.Equal(eventsBefore, eventsAfterNoop)
	s.Zero(statusChangedAfterNoop)

	s.Require().NoError(repo.Update(ctx, issue.ID, map[string]any{"status": types.StatusOpen, "title": "after"}, "tester", domain.IssueTableOpts{}))
	afterScalar, err := repo.Get(ctx, issue.ID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal("after", afterScalar.Title)
	s.NotEqual(afterNoop.RowVersion, afterScalar.RowVersion)
	var updated, statusChanged int
	s.Require().NoError(tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", issue.ID, string(types.EventUpdated)).Scan(&updated))
	s.Require().NoError(tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", issue.ID, string(types.EventStatusChanged)).Scan(&statusChanged))
	s.Equal(1, updated)
	s.Zero(statusChanged)
	s.Require().NoError(tx.Commit())
}

func (s *testSuite) issueUpdatePreservesCallerMap() {
	ctx := s.Ctx()
	issue := newTestIssue("preserve-caller-map", "before")
	s.Require().NoError(s.issueRepo().Insert(ctx, issue, "tester", domain.InsertIssueOpts{}))
	updates := map[string]any{"status": types.StatusOpen}

	s.Require().NoError(s.issueRepo().Update(ctx, issue.ID, updates, "tester", domain.IssueTableOpts{}))
	s.Equal(map[string]any{"status": types.StatusOpen}, updates)
}
