package uow

// bd-6dnrw.44 item 7: a failed DOLT_COMMIT used to release the pinned conn
// with the transaction still open; the next borrower's START TRANSACTION
// would implicitly commit the orphaned writes. These tests pin the repair
// sequence: rollback on commit failure, and poison the conn (pool discard,
// observable via db.Stats) when even the rollback fails.
//
// The hardening (commit 794ff0790) was reverted to BASE in a59e75325's serverv2
// triage, which left doltServerTx.Commit releasing the pinned session with its
// transaction still open on a non-transient DOLT_COMMIT failure — the exact
// late/double-apply hazard RunInTransaction now guards against.
// go-sql-driver v1.9.3 ResetSession only does a liveness
// check (no COM_RESET_CONNECTION), so an orphaned open tx on a pooled session is
// implicitly committed by the next borrower's START TRANSACTION.

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMockTxProvider(t *testing.T) (*doltSQLProvider, sqlmock.Sqlmock) {
	t.Helper()
	mockDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mockDB.Close() })
	return &doltSQLProvider{defaultBranch: defaultBranch, db: mockDB}, mock
}

func TestDoltServerTxCommitFailureRollsBackBeforeRelease(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DOLT_COMMIT").WillReturnError(errors.New("commit exploded"))
	mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	err = tx.Commit(context.Background(), "msg")
	require.ErrorContains(t, err, "commit exploded")
	require.NoError(t, mock.ExpectationsWereMet(), "ROLLBACK must run on the session before the conn is released")
	assert.Equal(t, 1, p.db.Stats().OpenConnections, "rolled-back session is clean and may return to the pool")
}

func TestDoltServerTxCommitAndRollbackFailurePoisonsConn(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DOLT_COMMIT").WillReturnError(errors.New("commit exploded"))
	mock.ExpectExec("ROLLBACK").WillReturnError(errors.New("rollback exploded too"))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	err = tx.Commit(context.Background(), "msg")
	require.ErrorContains(t, err, "commit exploded")
	assert.Equal(t, 0, p.db.Stats().OpenConnections, "session with an open tx must be discarded, not pooled")
}

func TestDoltServerTxRollbackFailurePoisonsConn(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("ROLLBACK").WillReturnError(errors.New("rollback exploded"))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	err = tx.Rollback(context.Background())
	require.ErrorContains(t, err, "rollback exploded")
	assert.Equal(t, 0, p.db.Stats().OpenConnections, "session with an open tx must be discarded, not pooled")
}

func TestBeginTxStartTransactionFailureReleasesConn(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnError(errors.New("no tx for you"))

	_, err := p.BeginTx(context.Background())
	require.ErrorContains(t, err, "no tx for you")
	assert.Equal(t, 0, p.db.Stats().InUse, "pinned conn must not leak when START TRANSACTION fails")
}
