package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestEventsSQLRepository() {
	s.Run("Record", func() {
		s.Run("DoesNotWriteEventsTable", s.eventsRecordDoesNotWriteEvents)
		s.Run("DoesNotWriteWispEventsTable", s.eventsRecordDoesNotWriteWispEvents)
		s.Run("MissingIssueIDNoops", s.eventsRecordMissingIssueNoops)
	})
}

func (s *testSuite) eventsRepo() domain.EventsSQLRepository {
	return NewEventsSQLRepository(s.Runner())
}

func (s *testSuite) seedIssueRow(id string) {
	_, err := s.Runner().ExecContext(s.Ctx(), `
		INSERT INTO issues (id, title, description, design, acceptance_criteria, notes)
		VALUES (?, ?, '', '', '', '')
	`, id, "seed")
	s.Require().NoError(err)
}

// seedWispRow inserts a minimal row into the wisps table. The wisps schema
// has defaults for the TEXT columns (unlike issues), so id + title is enough.
func (s *testSuite) seedWispRow(id string) {
	_, err := s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO wisps (id, title) VALUES (?, ?)",
		id, "seed-wisp")
	s.Require().NoError(err)
}

func (s *testSuite) seedLegacyEventRow(issueID string, eventType types.EventType, useWisps bool) {
	table := "events"
	if useWisps {
		table = "wisp_events"
	}
	//nolint:gosec // G201: table is selected from two hardcoded constants.
	_, err := s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO "+table+" (id, issue_id, event_type, actor) VALUES (?, ?, ?, ?)",
		issueops.NewEventID(), issueID, string(eventType), "tester")
	s.Require().NoError(err)
}

func (s *testSuite) eventsRecordDoesNotWriteEvents() {
	s.seedIssueRow("bd-evt-1")

	r := s.eventsRepo()
	s.Require().NoError(r.Record(s.Ctx(), domain.Event{
		IssueID: "bd-evt-1",
		Type:    types.EventCreated,
		Actor:   "tester",
	}, domain.RecordEventOpts{}))

	var count int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ?",
		"bd-evt-1",
	).Scan(&count))
	s.Equal(0, count, "Record must not append audit rows to events")

	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?",
		"bd-evt-1",
	).Scan(&count))
	s.Equal(0, count, "no row should be in wisp_events")
}

func (s *testSuite) eventsRecordDoesNotWriteWispEvents() {
	r := s.eventsRepo()
	s.Require().NoError(r.Record(s.Ctx(), domain.Event{
		IssueID: "bd-evt-wisp",
		Type:    types.EventUpdated,
		Actor:   "tester",
	}, domain.RecordEventOpts{UseWispsTable: true}))

	var count int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?",
		"bd-evt-wisp",
	).Scan(&count))
	s.Equal(0, count, "Record must not append audit rows to wisp_events")

	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ?",
		"bd-evt-wisp",
	).Scan(&count))
	s.Equal(0, count, "no row should be in events table")
}

func (s *testSuite) eventsRecordMissingIssueNoops() {
	r := s.eventsRepo()
	s.Require().NoError(r.Record(s.Ctx(), domain.Event{
		IssueID: "bd-evt-no-such-issue",
		Type:    types.EventCreated,
		Actor:   "tester",
	}, domain.RecordEventOpts{}))
}
