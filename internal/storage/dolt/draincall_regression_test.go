package dolt

import (
	"context"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// TestDoltAddAndCommitDrainsCallResultSets pins doltAddAndCommit to routing its
// CALL DOLT_ADD/CALL DOLT_COMMIT pair through schema.DrainCall (QueryContext +
// a deferred Close) instead of plain ExecContext. See schema.DrainCall's doc
// comment for why: a CALL that errors before go-sql-driver/mysql's own
// handleOk.discardResults() runs leaves its result set unread on the wire,
// and a pinned connection returned to the pool in that state poisons whoever
// borrows it next ("busy buffer" -> "driver: bad connection").
//
// sqlmock only satisfies an ExpectQuery expectation for a driver Query call
// (QueryContext), not for Exec (ExecContext) — go-sqlmock's ExpectExec and
// ExpectQuery are deliberately distinct expectation types. A future edit
// that reverts either call in doltAddAndCommit back to tx.ExecContext /
// conn.ExecContext therefore fails this test loudly ("call to ExecQuery
// was not expected, next expectation is: ExecQuery ...") instead of
// silently reintroducing the drain gap.
func TestDoltAddAndCommitDrainsCallResultSets(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
		WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
		WithArgs("bd: test commit", " <>").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))

	store := &DoltStore{db: db}

	if err := store.doltAddAndCommit(context.Background(), []string{"issues"}, "bd: test commit"); err != nil {
		t.Fatalf("doltAddAndCommit: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}
