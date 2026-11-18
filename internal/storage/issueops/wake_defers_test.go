package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/sqlcount"
)

// With versioned history off the mint is a no-op, so the sweep has no reason
// to look at an expired row before it wakes it, nor after. With history on it
// reads the woken row to mint its version; a later change that also checks
// each row against the mint before waking it (so that one unversionable row
// cannot abort the whole sweep) must keep that check free when history is
// off.
//
// The mock scripts exactly the statements a one-row sweep issues: the
// expired-row SELECT, the UPDATE, the event's same-content SELECT and its
// INSERT, and the empty wisps pass. Nothing else is expected, and the mock is
// reached through sqlcount, which counts every statement the sweep sends. So
// a sweep that loaded the row (or read its participation) before or after
// waking it fails the pin even if it swallowed the error sqlmock answers that
// statement with, which sqlmock's own checks would not see. The history-on
// run of the same script is the control: the mint's read must make the pin
// refuse it.
func TestWakeExpiredDefersWithHistoryOffLoadsNothingBeforeWaking(t *testing.T) {
	if err := wakeAgainstScript(t, false); err != nil {
		t.Fatalf("history off: %v", err)
	}
	t.Run("history on is refused by the same script", func(t *testing.T) {
		err := wakeAgainstScript(t, true)
		if err == nil {
			t.Fatal("the sweep with history on passed the history-off script; " +
				"the script no longer sees the reads history adds, so the history-off run above proves nothing")
		}
		t.Logf("history on, as expected: %v", err)
	})
}

// wakeStatements is how many statements the script below holds, and so how
// many the history-off sweep may send.
const wakeStatements = 5

// wakeAgainstScript runs a one-row sweep on a mocked transaction scoped with
// versioned history as asked, against the script of the statements the
// history-off sweep issues. It returns why the sweep broke the script, or nil
// if it sent exactly the scripted statements and woke the scripted row.
func wakeAgainstScript(t *testing.T, history bool) error {
	t.Helper()
	mock, tx, counts := countedMockTx(t)
	if history {
		t.Cleanup(ScopeVersionedHistoryTransaction(tx, true))
	}
	// Otherwise tx is not scoped for versioned history, which is how every
	// transaction reads while it is off.
	mock.ExpectQuery(`SELECT id FROM issues WHERE status = 'deferred'`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-1"))
	mock.ExpectExec(`UPDATE issues SET status = 'open', defer_until = NULL`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "bd-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id FROM events WHERE issue_id`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`INSERT INTO events`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id FROM wisps WHERE status = 'deferred'`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	got, err := WakeExpiredDefersInTx(t.Context(), tx)
	if err != nil {
		return fmt.Errorf("the sweep failed against the script: %w", err)
	}
	if n := counts.Total(); n != wakeStatements {
		return fmt.Errorf("the sweep sent %d statements, want exactly the %d scripted; "+
			"sqlmock refused each extra one with an error the sweep dropped", n, wakeStatements)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		return fmt.Errorf("the sweep did not send the scripted statements: %w", err)
	}
	if !slices.Equal(got.Issues, []string{"bd-1"}) || len(got.Wisps) != 0 {
		return fmt.Errorf("woken = issues %v, wisps %v, want [bd-1] and none", got.Issues, got.Wisps)
	}
	return nil
}

// TestExpiredDefersDue pins the preflight's answers and statements: one
// statement over both tables, the issues-only form on a table-not-found error
// that does not name issues, and any other error returned rather than read as
// "nothing due".
func TestExpiredDefersDue(t *testing.T) {
	const both = `SELECT EXISTS \(SELECT 1 FROM issues WHERE status = 'deferred' AND defer_until IS NOT NULL AND defer_until <= UTC_TIMESTAMP\(\)\), ` +
		`EXISTS \(SELECT 1 FROM wisps WHERE status = 'deferred' AND defer_until IS NOT NULL AND defer_until <= UTC_TIMESTAMP\(\)\)`
	const issuesOnly = `^SELECT EXISTS \(SELECT 1 FROM issues WHERE status = 'deferred' AND defer_until IS NOT NULL AND defer_until <= UTC_TIMESTAMP\(\)\)$`
	missingWisps := errors.New("Error 1146 (42S02): table not found: wisps")
	missingIssues := errors.New("Error 1146 (42S02): table not found: issues")
	unnamedMissing := errors.New("Error 1146 (42S02): no such table")

	for _, tc := range []struct {
		name       string
		script     func(sqlmock.Sqlmock)
		want       bool
		wantErr    bool
		wantErrIs  error
		statements int64
	}{
		{
			name: "nothing due",
			script: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(both).WillReturnRows(sqlmock.NewRows([]string{"i", "w"}).AddRow(0, 0))
			},
			statements: 1,
		},
		{
			name: "an issue due",
			script: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(both).WillReturnRows(sqlmock.NewRows([]string{"i", "w"}).AddRow(1, 0))
			},
			want:       true,
			statements: 1,
		},
		{
			name: "a wisp due",
			script: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(both).WillReturnRows(sqlmock.NewRows([]string{"i", "w"}).AddRow(0, 1))
			},
			want:       true,
			statements: 1,
		},
		{
			name: "no wisps table, an issue due",
			script: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(both).WillReturnError(missingWisps)
				m.ExpectQuery(issuesOnly).WillReturnRows(sqlmock.NewRows([]string{"i"}).AddRow(1))
			},
			want:       true,
			statements: 2,
		},
		{
			name: "no wisps table, nothing due",
			script: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(both).WillReturnError(missingWisps)
				m.ExpectQuery(issuesOnly).WillReturnRows(sqlmock.NewRows([]string{"i"}).AddRow(0))
			},
			statements: 2,
		},
		{
			name: "no wisps table, issues-only form fails",
			script: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(both).WillReturnError(missingWisps)
				m.ExpectQuery(issuesOnly).WillReturnError(errors.New("Error 1146 (42S02): table not found: issues"))
			},
			wantErr:    true,
			statements: 2,
		},
		{
			name: "a missing issues table is returned, not retried in the issues-only form",
			script: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(both).WillReturnError(missingIssues)
			},
			wantErrIs:  missingIssues,
			statements: 1,
		},
		{
			// The sweep tolerates any table-not-found error from its wisps half,
			// so the preflight asks the issues-only form rather than skip a sweep
			// that would have woken issues.
			name: "a 1146 that names no table falls back to the issues-only form",
			script: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(both).WillReturnError(unnamedMissing)
				m.ExpectQuery(issuesOnly).WillReturnRows(sqlmock.NewRows([]string{"i"}).AddRow(1))
			},
			want:       true,
			statements: 2,
		},
		{
			name: "a 1146 that names no table, then a failing issues-only form, is returned",
			script: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(both).WillReturnError(unnamedMissing)
				m.ExpectQuery(issuesOnly).WillReturnError(missingIssues)
			},
			wantErrIs:  missingIssues,
			statements: 2,
		},
		{
			name: "an unrelated error is returned, not retried in the issues-only form",
			script: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(both).WillReturnError(errors.New("Error 1064 (42000): syntax error"))
			},
			wantErr:    true,
			statements: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock, tx, counts := countedMockTx(t)
			tc.script(mock)
			got, err := ExpiredDefersDue(t.Context(), tx)
			if (err != nil) != (tc.wantErr || tc.wantErrIs != nil) {
				t.Fatalf("ExpiredDefersDue error = %v, want error %v", err, tc.wantErr || tc.wantErrIs != nil)
			}
			if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
				t.Fatalf("ExpiredDefersDue error = %v, want it to wrap %v", err, tc.wantErrIs)
			}
			if got != tc.want {
				t.Errorf("ExpiredDefersDue = %v, want %v", got, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			if n := counts.Total(); n != tc.statements {
				t.Errorf("sent %d statements, want %d", n, tc.statements)
			}
		})
	}
}

var sqlmockSeq atomic.Int64

// countedMockTx begins a transaction on a fresh sqlmock connection that is
// reached through sqlcount, so a pin can count every statement the code
// under test sends, including one whose error it swallowed.
func countedMockTx(t *testing.T) (sqlmock.Sqlmock, *sql.Tx, *sqlcount.Counts) {
	t.Helper()
	dsn := fmt.Sprintf("issueops_counted_%d", sqlmockSeq.Add(1))
	mockDB, mock, err := sqlmock.NewWithDSN(dsn)
	if err != nil {
		t.Fatalf("sqlmock.NewWithDSN: %v", err)
	}
	counts := &sqlcount.Counts{}
	db := sql.OpenDB(sqlcount.WrapConnector(sqlmockConnector{dsn: dsn, driver: mockDB.Driver()}, counts))
	t.Cleanup(func() {
		_ = db.Close()
		_ = mockDB.Close()
	})
	mock.ExpectBegin()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return mock, tx, counts
}

// sqlmockConnector opens the sqlmock connection registered under dsn, for
// sql.OpenDB through a wrapping connector.
type sqlmockConnector struct {
	dsn    string
	driver driver.Driver
}

func (c sqlmockConnector) Connect(context.Context) (driver.Conn, error) { return c.driver.Open(c.dsn) }
func (c sqlmockConnector) Driver() driver.Driver                        { return c.driver }
