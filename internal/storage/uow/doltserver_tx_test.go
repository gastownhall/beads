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
	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/issueops"
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

// TestDoltServerTxCommitDefersDoltCommitUnderDeferredContext pins GH#4995:
// dolt.auto-commit=batch/off marks the context with WithDeferredVersionCommit.
// doltServerTx.Commit must blank the commit message, bypassing DOLT_COMMIT
// and the HasPendingChanges dolt_status check, and executing a plain COMMIT
// to persist writes into the working set without advancing Dolt history.
func TestDoltServerTxCommitDefersDoltCommitUnderDeferredContext(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deferred bool
	}{
		{name: "on (not deferred)", deferred: false},
		{name: "batch or off (deferred)", deferred: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, mock := newMockTxProvider(t)
			mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
			if tc.deferred {
				// With deferred version commit (GH#4995), the non-empty message is blanked,
				// skipping DOLT_COMMIT and executing plain COMMIT without status check.
				mock.ExpectExec(matchPlainCommit).WillReturnResult(sqlmock.NewResult(0, 0))
			} else {
				expectPendingChanges(mock, 1)
				mock.ExpectExec("DOLT_COMMIT").WithArgs("bd: real write").WillReturnResult(sqlmock.NewResult(0, 1))
			}

			tx, err := p.BeginTx(context.Background())
			require.NoError(t, err)

			ctx := context.Background()
			if tc.deferred {
				ctx = issueops.WithDeferredVersionCommit(ctx)
			}

			err = tx.Commit(ctx, "bd: real write")
			require.NoError(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
			assert.Equal(t, 1, p.db.Stats().OpenConnections, "session must cleanly return to pool")
		})
	}
}

// TestDoltServerTxCommitHonorsImmediateVersionCommit pins the other half of the
// GH#4995 policy: the proxied route applies the deferral ONCE, to the root
// context, so the explicit commit points — the duals whose direct-route twin
// calls transact and mints a Dolt commit whatever dolt.auto-commit says — opt
// back out with issueops.WithImmediateVersionCommit. The caller's message must
// then survive all the way to DOLT_COMMIT; blanking it would persist the rows
// and record nothing, which is what `bd batch -m` must never do.
func TestDoltServerTxCommitHonorsImmediateVersionCommit(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	expectPendingChanges(mock, 1)
	mock.ExpectExec("DOLT_COMMIT").WithArgs("bd: batch 3 ops by tester").WillReturnResult(sqlmock.NewResult(0, 1))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	// Composed in the order the proxied CLI composes them: the route-wide
	// deferral from the pre-run, then the exemption from the dual.
	ctx := issueops.WithImmediateVersionCommit(issueops.WithDeferredVersionCommit(context.Background()))
	require.NoError(t, tx.Commit(ctx, "bd: batch 3 ops by tester"))
	require.NoError(t, mock.ExpectationsWereMet())
	assert.Equal(t, 1, p.db.Stats().OpenConnections, "session must cleanly return to pool")
}

// TestDoltServerTxRunTxWithDeferredContextSkipsDoltCommit verifies that a complete
// unit of work executed via RunTx under deferred version commit commits via plain
// COMMIT and succeeds without advancing Dolt history (GH#4995).
func TestDoltServerTxRunTxWithDeferredContextSkipsDoltCommit(t *testing.T) {
	p, mock := newMockTxProvider(t)
	// RunTx is a committing unit of work, so it takes the journal writer lock
	// with the journal off too.
	mock.ExpectQuery(matchGetLock).
		WithArgs(issueops.JournalWriterLockName(""), issueops.JournalWriterLockWait).
		WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(matchPlainCommit).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(matchReleaseLock).
		WithArgs(issueops.JournalWriterLockName("")).
		WillReturnResult(sqlmock.NewResult(0, 0))

	ctx := issueops.WithDeferredVersionCommit(context.Background())
	err := RunTx(ctx, p, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "bd: update GH#4995", nil
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	assert.Equal(t, 1, p.db.Stats().OpenConnections)
}

// matchHasStaged matches the staged-set check issueops.HasStagedChanges runs
// before CommitScoped's DOLT_COMMIT('-m').
const matchHasStaged = `SELECT COUNT\(\*\) FROM dolt_status WHERE staged = 1`

// expectScopedDoltCommit arms one attempt of CommitScoped's Dolt commit: a
// DOLT_ADD per table, the staged-set check reporting staged rows, and, when
// something is staged, a DOLT_COMMIT('-m') answering commitErr.
func expectScopedDoltCommit(mock sqlmock.Sqlmock, tables []string, staged int, message string, commitErr error) {
	for _, table := range tables {
		mock.ExpectQuery(`CALL DOLT_ADD\(\?\)`).WithArgs(table).
			WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(0))
	}
	mock.ExpectQuery(matchHasStaged).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(staged))
	if staged == 0 {
		return
	}
	commit := mock.ExpectQuery(`CALL DOLT_COMMIT\('-m', \?\)`).WithArgs(message)
	if commitErr != nil {
		commit.WillReturnError(commitErr)
	} else {
		commit.WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("c0ffee"))
	}
}

// TestDoltServerTxCommitScopedCommitsSQLBeforeDoltCommit pins the
// LatentLabsSpace/NEXUS#92 ordering on the uow leg: CommitScoped ends the SQL
// transaction with a plain COMMIT and only then stages and Dolt-commits its
// tables, on the same pinned session. Staged inside the open transaction,
// DOLT_ADD builds the Dolt commit from the transaction's start snapshot and
// writes a concurrent writer's committed rows back to their old values.
func TestDoltServerTxCommitScopedCommitsSQLBeforeDoltCommit(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(matchPlainCommit).WillReturnResult(sqlmock.NewResult(0, 0))
	expectScopedDoltCommit(mock, []string{"events", "issues"}, 2, "bd: re-arm lease x", nil)

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	require.NoError(t, tx.(ScopedCommitter).CommitScoped(context.Background(), []string{"events", "issues"}, "bd: re-arm lease x"))
	require.NoError(t, mock.ExpectationsWereMet(), "the SQL COMMIT must come first, then DOLT_ADD per table and DOLT_COMMIT('-m')")
	assert.Equal(t, 1, p.db.Stats().OpenConnections, "a cleanly committed session may return to the pool")
	require.ErrorContains(t, tx.Commit(context.Background(), ""), "already done")
}

// TestDoltServerTxCommitScopedSwallowsAFailedDoltCommit: once the SQL COMMIT
// has landed the write is durable, so a Dolt commit that fails after it must
// not fail CommitScoped (RunTxScoped would replay a committed unit of work).
// The session, whose state the failed statement leaves unknown, is discarded.
func TestDoltServerTxCommitScopedSwallowsAFailedDoltCommit(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(matchPlainCommit).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`CALL DOLT_ADD\(\?\)`).WithArgs("issues").WillReturnError(errors.New("dolt add exploded"))

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	require.NoError(t, tx.(ScopedCommitter).CommitScoped(context.Background(), []string{"issues"}, "bd: re-arm lease x"),
		"a committed write must not be reported as failed")
	require.NoError(t, mock.ExpectationsWereMet(), "a failure other than a conflict is not retried")
	assert.Equal(t, 0, p.db.Stats().OpenConnections, "the session must be discarded, not pooled")
	require.ErrorContains(t, tx.Commit(context.Background(), ""), "already done")
}

// TestDoltServerTxCommitScopedRetriesAConflictedDoltCommit: outside a
// transaction, a Dolt commit that conflicts with a concurrent writer is rolled
// back as an @@autocommit merge conflict (1105), not 1213, and is retried like
// one.
func TestDoltServerTxCommitScopedRetriesAConflictedDoltCommit(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(matchPlainCommit).WillReturnResult(sqlmock.NewResult(0, 0))
	conflict := &mysql.MySQLError{Number: 1105, Message: "Merge conflict detected, @autocommit transaction rolled back"}
	expectScopedDoltCommit(mock, []string{"issues"}, 1, "bd: re-arm lease x", conflict)
	expectScopedDoltCommit(mock, []string{"issues"}, 1, "bd: re-arm lease x", nil)

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	require.NoError(t, tx.(ScopedCommitter).CommitScoped(context.Background(), []string{"issues"}, "bd: re-arm lease x"))
	require.NoError(t, mock.ExpectationsWereMet(), "the conflicted attempt must be staged and committed again")
	assert.Equal(t, 1, p.db.Stats().OpenConnections, "a session whose retry committed may return to the pool")
}

// TestDoltServerTxCommitScopedReturnsASerializationFailure: a serialization
// failure of the SQL COMMIT is returned for RunTxScoped to redo the whole unit
// of work, and no Dolt commit follows a COMMIT that did not land.
func TestDoltServerTxCommitScopedReturnsASerializationFailure(t *testing.T) {
	p, mock := newMockTxProvider(t)
	mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(matchPlainCommit).WillReturnError(&mysql.MySQLError{Number: 1213, Message: "serialization failure"})

	tx, err := p.BeginTx(context.Background())
	require.NoError(t, err)

	err = tx.(ScopedCommitter).CommitScoped(context.Background(), []string{"issues"}, "bd: re-arm lease x")
	require.True(t, isSerializationError(err), "err = %v, want the serialization failure", err)
	require.NoError(t, mock.ExpectationsWereMet(), "nothing may be staged or committed after a failed COMMIT")
}

// TestDoltServerTxCommitScopedMakesNoEmptyDoltCommit: a concurrent writer's
// Dolt commit can take this transaction's rows before its own Dolt commit
// runs, seen either before the commit (nothing staged) or by it ("nothing to
// commit"). Neither is a failure: unlike Commit's in-transaction commit, the
// rows are already committed.
func TestDoltServerTxCommitScopedMakesNoEmptyDoltCommit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		staged    int
		commitErr error
	}{
		{name: "nothing staged", staged: 0},
		{name: "nothing to commit", staged: 1, commitErr: errors.New("nothing to commit")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, mock := newMockTxProvider(t)
			mock.ExpectExec("START TRANSACTION").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec(matchPlainCommit).WillReturnResult(sqlmock.NewResult(0, 0))
			expectScopedDoltCommit(mock, []string{"issues"}, tc.staged, "bd: re-arm lease x", tc.commitErr)

			tx, err := p.BeginTx(context.Background())
			require.NoError(t, err)

			require.NoError(t, tx.(ScopedCommitter).CommitScoped(context.Background(), []string{"issues"}, "bd: re-arm lease x"))
			require.NoError(t, mock.ExpectationsWereMet())
			assert.Equal(t, 1, p.db.Stats().OpenConnections, "the session is clean and may return to the pool")
		})
	}
}
