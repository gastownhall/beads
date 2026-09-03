package issueops

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

func TestReopenIssueInTxRetriesConditionalUpdateWhenLatestStatusIsCustomDone(t *testing.T) {
	db, mock, tx := beginMockTx(t)
	defer db.Close()

	const id = "bd-reopen-race"
	emptyRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"issue_id"})
	}
	expectInactiveWisp := func() {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
			WithArgs(id).
			WillReturnError(sql.ErrNoRows)
	}
	expectStatus := func(status types.Status) {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM issues WHERE id = ?")).
			WithArgs(id).
			WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(string(status)))
	}
	expectAffectedQueries := func() {
		for range 9 {
			mock.ExpectQuery("(?s).*").WillReturnRows(emptyRows())
		}
	}

	expectInactiveWisp()
	expectStatus(types.StatusClosed)
	expectAffectedQueries()
	mock.ExpectExec(`(?s)UPDATE issues\s+SET status`).WillReturnResult(sqlmock.NewResult(0, 0))
	expectStatus("archived")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
		WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("archived", string(types.CategoryDone)))

	// A fresh custom done status after the conditional miss must be retried, not
	// reported as an unchanged reopen.
	expectInactiveWisp()
	expectStatus("archived")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
		WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("archived", string(types.CategoryDone)))
	expectAffectedQueries()
	mock.ExpectExec(`(?s)UPDATE issues\s+SET status`).
		WithArgs(types.StatusOpen, sqlmock.AnyArg(), sqlmock.AnyArg(), id, types.Status("archived")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM leases WHERE issue_id = ?")).WithArgs(id).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`(?s)SELECT id FROM events`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`(?s)INSERT INTO events`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE issues i SET`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)UPDATE issues i SET`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	result, err := ReopenIssueInTx(context.Background(), tx, id, "", "tester")
	if err != nil {
		t.Fatalf("ReopenIssueInTx: %v", err)
	}
	if !result.Changed {
		t.Fatal("ReopenIssueInTx reported Changed=false after the latest status remained done")
	}
	if !result.IssueRowsChanged {
		t.Fatal("permanent reopen must report its concrete issues-row update")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestReopenIssueInTxCustomNonDoneIsNotAlreadyOpen(t *testing.T) {
	db, mock, tx := beginMockTx(t)
	defer db.Close()

	const id = "bd-reopen-custom-active"
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WithArgs(id).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM issues WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("triaged"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
		WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("triaged", string(types.CategoryActive)))
	mock.ExpectRollback()

	result, err := ReopenIssueInTx(context.Background(), tx, id, "", "tester")
	if err != nil {
		t.Fatalf("ReopenIssueInTx: %v", err)
	}
	if result.Changed || result.AlreadyOpen || result.IssueRowsChanged {
		t.Fatalf("result = %+v, want unchanged and not already open", result)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestReopenIssueInTxConditionalMissToCustomNonDoneIsNotAlreadyOpen(t *testing.T) {
	db, mock, tx := beginMockTx(t)
	defer db.Close()

	const id = "bd-reopen-race-active"
	emptyRows := func() *sqlmock.Rows { return sqlmock.NewRows([]string{"issue_id"}) }
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WithArgs(id).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM issues WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(string(types.StatusClosed)))
	for range 9 {
		mock.ExpectQuery("(?s).*").WillReturnRows(emptyRows())
	}
	mock.ExpectExec(`(?s)UPDATE issues\s+SET status`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM issues WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("triaged"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
		WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("triaged", string(types.CategoryActive)))
	mock.ExpectRollback()

	result, err := ReopenIssueInTx(context.Background(), tx, id, "ignored", "tester")
	if err != nil {
		t.Fatalf("ReopenIssueInTx: %v", err)
	}
	if result.Changed || result.AlreadyOpen || result.IssueRowsChanged {
		t.Fatalf("result = %+v, want unchanged and not already open", result)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected lease, event, comment, or recompute write: %v", err)
	}
}

func TestReopenIssueInTxReportsConcurrentChangeAfterRetryExhaustion(t *testing.T) {
	db, mock, tx := beginMockTx(t)
	defer db.Close()

	const id = "bd-reopen-race-exhausted"
	emptyRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"issue_id"})
	}
	expectInactiveWisp := func() {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
			WithArgs(id).
			WillReturnError(sql.ErrNoRows)
	}
	expectStatus := func() {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM issues WHERE id = ?")).
			WithArgs(id).
			WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(string(types.StatusClosed)))
	}
	expectAffectedQueries := func() {
		for range 9 {
			mock.ExpectQuery("(?s).*").WillReturnRows(emptyRows())
		}
	}
	expectAttempt := func() {
		expectInactiveWisp()
		expectStatus()
		expectAffectedQueries()
		mock.ExpectExec(`(?s)UPDATE issues\s+SET status`).WillReturnResult(sqlmock.NewResult(0, 0))
		expectStatus()
	}

	expectAttempt()
	expectAttempt()
	mock.ExpectRollback()

	_, err := ReopenIssueInTx(context.Background(), tx, id, "", "tester")
	if err == nil || !regexp.MustCompile("status changed concurrently").MatchString(err.Error()) {
		t.Fatalf("err = %v, want bounded concurrent-change error", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
