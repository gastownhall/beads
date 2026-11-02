package schema

import (
	"regexp"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectTransactionCommitRead mocks suspendTransactionCommit's read of the
// session's @@dolt_transaction_commit, which a needed migration pass issues
// right after migrationWorkNeeded. 0 (the default) means no SET follows.
func expectTransactionCommitRead(mock sqlmock.Sqlmock, value int) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT @@SESSION.dolt_transaction_commit")).
		WillReturnRows(sqlmock.NewRows([]string{"@@SESSION.dolt_transaction_commit"}).AddRow(value))
}
