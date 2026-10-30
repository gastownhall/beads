package issueops

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// errTransport stands in for a connection that dies mid-result-set: to
// database/sql, Rows.Next returns false exactly as it does at end-of-rows,
// and only Rows.Err distinguishes a truncated read from a complete one.
var errTransport = errors.New("transport: connection reset mid-read")

// TestRowIterationReportsMidIterationFailure pins the issueops readers whose
// answers are acted on: when a row iteration fails part-way through, the
// reader must return that error, never an answer computed from the rows read
// before the failure. Each subtest targets one loop, makes its iteration
// fail at the driver after zero or more rows, and registers no statement
// after the target read — a reader that presses on hits sqlmock's
// unexpected-call error instead, which is not errTransport either.
func TestRowIterationReportsMidIterationFailure(t *testing.T) {
	// GetEpicsEligibleForClosureInTx decides which epics `bd epic
	// close-eligible` may close. It takes DBTX, so it runs directly on the
	// mocked DB without a transaction.
	t.Run("epics/open-epic-ids", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectQuery("SELECT id FROM issues").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).
				AddRow("bd-epic-1").
				AddRow("bd-epic-2").
				RowError(1, errTransport))

		epics, err := GetEpicsEligibleForClosureInTx(context.Background(), db)
		if !errors.Is(err, errTransport) {
			t.Fatalf("iteration failed part-way through and no error was returned (epics=%v, err=%v): the caller cannot tell a truncated result from a complete one", epics, err)
		}
		if epics != nil {
			t.Fatalf("eligible epics computed from a truncated read: %v", epics)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	// The same read failing on the first row: without the check the empty
	// epic set takes the len == 0 early return and reads as "nothing to do".
	t.Run("epics/open-epic-ids-first-row", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectQuery("SELECT id FROM issues").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).
				AddRow("bd-epic-1").
				RowError(0, errTransport))

		epics, err := GetEpicsEligibleForClosureInTx(context.Background(), db)
		if !errors.Is(err, errTransport) {
			t.Fatalf("iteration failed on the first row and no error was returned (epics=%v, err=%v): the caller cannot tell a truncated result from a complete one", epics, err)
		}
		if epics != nil {
			t.Fatalf("eligible epics computed from a truncated read: %v", epics)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	// A lost edge to an open child leaves the epic looking childless or
	// fully closed, so :166 would mark it EligibleForClose.
	t.Run("epics/edges-dependencies", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectQuery("SELECT id FROM issues").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-epic"))
		mock.ExpectQuery("AS parent_id, issue_id FROM dependencies").
			WillReturnRows(sqlmock.NewRows([]string{"parent_id", "issue_id"}).
				AddRow("bd-epic", "bd-child-1").
				AddRow("bd-epic", "bd-child-2").
				RowError(1, errTransport))

		epics, err := GetEpicsEligibleForClosureInTx(context.Background(), db)
		if !errors.Is(err, errTransport) {
			t.Fatalf("iteration failed part-way through and no error was returned (epics=%v, err=%v): the caller cannot tell a truncated result from a complete one", epics, err)
		}
		if epics != nil {
			t.Fatalf("eligible epics computed from a truncated read: %v", epics)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	// Wisp children keep their parent-child edges in wisp_dependencies, so
	// the dependencies read must complete first for this loop to be reached.
	t.Run("epics/edges-wisp_dependencies", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectQuery("SELECT id FROM issues").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-epic"))
		mock.ExpectQuery("AS parent_id, issue_id FROM dependencies").
			WillReturnRows(sqlmock.NewRows([]string{"parent_id", "issue_id"}).AddRow("bd-epic", "bd-child-1"))
		mock.ExpectQuery("AS parent_id, issue_id FROM wisp_dependencies").
			WillReturnRows(sqlmock.NewRows([]string{"parent_id", "issue_id"}).
				AddRow("bd-epic", "bd-wisp-1").
				AddRow("bd-epic", "bd-wisp-2").
				RowError(1, errTransport))

		epics, err := GetEpicsEligibleForClosureInTx(context.Background(), db)
		if !errors.Is(err, errTransport) {
			t.Fatalf("iteration failed part-way through and no error was returned (epics=%v, err=%v): the caller cannot tell a truncated result from a complete one", epics, err)
		}
		if epics != nil {
			t.Fatalf("eligible epics computed from a truncated read: %v", epics)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	// A lost status row counts an open child as not closed, under-reporting
	// ClosedChildren on a nil error.
	t.Run("epics/child-statuses-issues", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectQuery("SELECT id FROM issues").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-epic"))
		mock.ExpectQuery("AS parent_id, issue_id FROM dependencies").
			WillReturnRows(sqlmock.NewRows([]string{"parent_id", "issue_id"}).
				AddRow("bd-epic", "bd-child-1").
				AddRow("bd-epic", "bd-child-2"))
		mock.ExpectQuery("AS parent_id, issue_id FROM wisp_dependencies").
			WillReturnRows(sqlmock.NewRows([]string{"parent_id", "issue_id"}))
		mock.ExpectQuery("FROM issues WHERE id IN").
			WillReturnRows(sqlmock.NewRows([]string{"id", "status", "close_reason"}).
				AddRow("bd-child-1", "closed", "").
				AddRow("bd-child-2", "open", "").
				RowError(1, errTransport))

		epics, err := GetEpicsEligibleForClosureInTx(context.Background(), db)
		if !errors.Is(err, errTransport) {
			t.Fatalf("iteration failed part-way through and no error was returned (epics=%v, err=%v): the caller cannot tell a truncated result from a complete one", epics, err)
		}
		if epics != nil {
			t.Fatalf("eligible epics computed from a truncated read: %v", epics)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}
