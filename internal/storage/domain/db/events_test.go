package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestEventsSQLRepository() {
	s.Run("Record", func() {
		s.Run("WritesToEventsTable", s.eventsRecordEvents)
		s.Run("WritesToWispEventsTable", s.eventsRecordWispEvents)
		s.Run("FieldsRoundTrip", s.eventsRecordRoundTrip)
		s.Run("MissingIssueIDFailsFKConstraint", s.eventsRecordFKViolation)
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

func (s *testSuite) eventsRecordEvents() {
	s.seedIssueRow("bd-evt-1")

	r := s.eventsRepo()
	s.Require().NoError(r.Record(s.Ctx(), domain.Event{
		IssueID: "bd-evt-1",
		Type:    types.EventCreated,
		Actor:   "tester",
	}, domain.RecordEventOpts{}))

	var count int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-evt-1", string(types.EventCreated),
	).Scan(&count))
	s.Equal(1, count, "expected one row in events table")

	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?",
		"bd-evt-1",
	).Scan(&count))
	s.Equal(0, count, "no row should be in wisp_events")
}

func (s *testSuite) eventsRecordWispEvents() {
	r := s.eventsRepo()
	s.Require().NoError(r.Record(s.Ctx(), domain.Event{
		IssueID: "bd-evt-wisp",
		Type:    types.EventUpdated,
		Actor:   "tester",
	}, domain.RecordEventOpts{UseWispsTable: true}))

	var count int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ? AND event_type = ?",
		"bd-evt-wisp", string(types.EventUpdated),
	).Scan(&count))
	s.Equal(1, count, "expected one row in wisp_events table")

	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ?",
		"bd-evt-wisp",
	).Scan(&count))
	s.Equal(0, count, "no row should be in events table")
}

func (s *testSuite) eventsRecordRoundTrip() {
	s.seedIssueRow("bd-evt-rt")

	r := s.eventsRepo()
	in := domain.Event{
		IssueID:  "bd-evt-rt",
		Type:     types.EventStatusChanged,
		Actor:    "alice",
		OldValue: "open",
		NewValue: "in_progress",
	}
	s.Require().NoError(r.Record(s.Ctx(), in, domain.RecordEventOpts{}))

	var gotIssueID, gotType, gotActor, gotOld, gotNew string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT issue_id, event_type, actor, old_value, new_value FROM events WHERE issue_id = ?",
		in.IssueID,
	).Scan(&gotIssueID, &gotType, &gotActor, &gotOld, &gotNew))
	s.Equal(in.IssueID, gotIssueID)
	s.Equal(string(in.Type), gotType)
	s.Equal(in.Actor, gotActor)
	s.Equal(in.OldValue, gotOld)
	s.Equal(in.NewValue, gotNew)
}

func (s *testSuite) eventsRecordFKViolation() {
	r := s.eventsRepo()
	err := r.Record(s.Ctx(), domain.Event{
		IssueID: "bd-evt-no-such-issue",
		Type:    types.EventCreated,
		Actor:   "tester",
	}, domain.RecordEventOpts{})
	s.Require().Error(err, "expected FK violation when issue_id does not exist")
}
