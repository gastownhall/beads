package uow

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestDoltSQLProviderBeginTxDiscardsConnectionOnStartFailure(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	startErr := &mysql.MySQLError{Number: 1213, Message: "start transaction conflict"}
	mock.ExpectExec(regexp.QuoteMeta("START TRANSACTION;")).WillReturnError(startErr)

	provider := &doltSQLProvider{defaultBranch: defaultBranch, db: database}
	tx, err := provider.BeginTx(context.Background())
	require.Nil(t, tx)
	var mysqlErr *mysql.MySQLError
	require.ErrorAs(t, err, &mysqlErr)
	require.Equal(t, uint16(1213), mysqlErr.Number)
	require.Eventually(t, func() bool {
		stats := database.Stats()
		return stats.InUse == 0 && stats.OpenConnections == 0
	}, time.Second, 10*time.Millisecond, "failed START TRANSACTION leaked or pooled its pinned connection")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDoltServerTxDiscardsConnectionWhenRollbackFails(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	mock.ExpectExec(regexp.QuoteMeta("START TRANSACTION;")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("ROLLBACK;")).WillReturnError(errors.New("rollback transport failure"))

	provider := &doltSQLProvider{defaultBranch: defaultBranch, db: database}
	tx, err := provider.BeginTx(context.Background())
	require.NoError(t, err)
	require.ErrorContains(t, tx.Rollback(context.Background()), "rollback transport failure")
	require.Eventually(t, func() bool {
		stats := database.Stats()
		return stats.InUse == 0 && stats.OpenConnections == 0
	}, time.Second, 10*time.Millisecond, "failed rollback returned a dirty session to the pool")
	require.NoError(t, mock.ExpectationsWereMet())
}
