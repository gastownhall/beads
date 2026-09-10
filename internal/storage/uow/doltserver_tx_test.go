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

// matchHasPending matches the dolt_status working-set check issued by
// issueops.HasPendingChanges — the guard Commit runs before deciding whether
// to mint a Dolt commit (#4348 re-port).
const matchHasPending = `SELECT COUNT\(\*\) FROM dolt_status`

// expectPendingChanges arms the HasPendingChanges guard to report a dirty
// (count > 0) working set, letting Commit proceed to DOLT_COMMIT.
func expectPendingChanges(mock sqlmock.Sqlmock, count int) {
	mock.ExpectQuery(matchHasPending).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
}

func TestDoltServerTxCommitFailureRollsBackBeforeRelease(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	expectPendingChanges(mock, 1)
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
	expectPendingChanges(mock, 1)
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

// The tests below pin the #4348 re-port (original commit 3bd52c27f): Commit
// gates DOLT_COMMIT('-Am') on issueops.HasPendingChanges so an idempotent
// write that staged nothing (same-value REPLACE INTO metadata, 0-row CAS
// re-claim, per-tick orchestrator reconciles) no longer issues a
// guaranteed-empty commit that Dolt rejects server-side with "nothing to
// commit" — flooding the server log and burning CPU. The skip path must still
// CLOSE the open SQL transaction (plain COMMIT) before the pinned connection
// is released; releasing with START TRANSACTION open would hand the next
// borrower an implicit commit of orphaned state.

// matchPlainCommit matches ONLY the plain SQL COMMIT (the ephemeral/skip
// form), never CALL DOLT_COMMIT(...).
const matchPlainCommit = `^COMMIT;$`

func TestDoltServerTxCommitSkipsEmptyDoltCommit(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	// Clean working set: the guard reports nothing pending…
	expectPendingChanges(mock, 0)
	// …so the tx must close with a plain COMMIT. Deliberately NO expectation
	// for DOLT_COMMIT: if the guard is reverted, the unconditional
	// CALL DOLT_COMMIT('-Am', ?) fails to match matchPlainCommit and this test
	// fails.
	mock.ExpectExec(matchPlainCommit).WillReturnResult(sqlmock.NewResult(0, 0))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	err = tx.Commit(context.Background(), "bd: noop")
	require.NoError(t, err, "Commit on a clean working set must skip DOLT_COMMIT and succeed")
	require.NoError(t, mock.ExpectationsWereMet(), "skip path must still close the SQL transaction with a plain COMMIT")
	assert.Equal(t, 1, p.db.Stats().OpenConnections, "cleanly closed session may return to the pool")

	err = tx.Commit(context.Background(), "bd: again")
	require.ErrorContains(t, err, "already done", "skip path must still mark the tx done")
}

func TestDoltServerTxCommitIssuesDoltCommitWhenPending(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	expectPendingChanges(mock, 3)
	mock.ExpectExec("DOLT_COMMIT").WithArgs("bd: real write").WillReturnResult(sqlmock.NewResult(0, 1))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	err = tx.Commit(context.Background(), "bd: real write")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet(), "a dirty working set must be committed via DOLT_COMMIT")
}

func TestDoltServerTxCommitPendingCheckFailureRollsBackBeforeRelease(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(matchHasPending).WillReturnError(errors.New("status check exploded"))
	mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	err = tx.Commit(context.Background(), "msg")
	require.ErrorContains(t, err, "status check exploded")
	require.NoError(t, mock.ExpectationsWereMet(), "a failed pending check leaves the tx open and must ROLLBACK before release")
	assert.Equal(t, 1, p.db.Stats().OpenConnections, "rolled-back session is clean and may return to the pool")
}

// TestDoltServerTxCommitPropagatesNothingToCommit pins the deliberate
// deviation from the original #4348 commit: a residual "nothing to commit"
// from DOLT_COMMIT itself is NOT swallowed here. RunTx already swallows it at
// the call site (tx.go), and the uow layer lets it surface as a lost-update
// signal (lostupdate_dolt_test.go) — swallowing it a layer down could mask a
// silently lost write.
func TestDoltServerTxCommitPropagatesNothingToCommit(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	expectPendingChanges(mock, 1)
	mock.ExpectExec("DOLT_COMMIT").WillReturnError(errors.New("nothing to commit"))
	mock.ExpectExec("ROLLBACK").WillReturnResult(sqlmock.NewResult(0, 0))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	err = tx.Commit(context.Background(), "msg")
	require.ErrorContains(t, err, "nothing to commit", "the lost-update signal must surface to the caller, not be swallowed here")
	require.NoError(t, mock.ExpectationsWereMet(), "the failed DOLT_COMMIT leaves the tx open and must ROLLBACK before release")
}

// TestDoltServerTxEphemeralCommitSkipsPendingCheck pins that the empty-message
// EPHEMERAL form (bd-aq0ql) bypasses the HasPendingChanges guard entirely: it
// already never mints a Dolt commit, so a dolt_status round-trip would be pure
// overhead on the lease-heartbeat path.
func TestDoltServerTxEphemeralCommitSkipsPendingCheck(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	// No dolt_status query expected: an added guard round-trip would surface
	// as an unexpected query and fail Commit.
	mock.ExpectExec(matchPlainCommit).WillReturnResult(sqlmock.NewResult(0, 0))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	err = tx.Commit(context.Background(), "")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
