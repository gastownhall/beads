package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// cliMergeSession hands the CLI pull route's merge (mergeTrackingRefAfterCLIFetch)
// a sqlmock database in place of the long-timeout connection openCLIMergeDB
// opens in production, for the rest of the test, and returns its mock. The
// merge gets a fresh *sql.DB over the mock's DSN with no idle connection, as
// openLongTimeoutConn's is: sqlmock.New pings, and the idle connection that
// leaves would be closed by the merge's SetMaxIdleConns(0). A test that uses
// it must not run in parallel: openCLIMergeDB is package state.
func cliMergeSession(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	dsn := fmt.Sprintf("cli-merge-session-%p", t)
	owner, mock, err := sqlmock.NewWithDSN(dsn, sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	prev := openCLIMergeDB
	openCLIMergeDB = func(*DoltStore) (*sql.DB, error) { return sql.Open("sqlmock", dsn) }
	t.Cleanup(func() { openCLIMergeDB = prev })
	return mock
}

func activeBranchRow(branch string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"active_branch()"}).AddRow(branch)
}

// doltMergeRow is a CALL DOLT_MERGE result row whose conflicts flag is
// conflicts.
func doltMergeRow(conflicts int) *sqlmock.Rows {
	message := doltMergeSucceededMessage
	if conflicts != 0 {
		message = "conflicts found"
	}
	return sqlmock.NewRows([]string{"hash", "fast_forward", "conflicts", "message"}).
		AddRow("abc", 0, conflicts, message)
}

// conflictRows is a dolt_conflicts result naming table, or none when table is
// empty.
func conflictRows(table string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"table", "num_conflicts"})
	if table != "" {
		rows.AddRow(table, 1)
	}
	return rows
}

// expectCleanSettleReads expects settleMergeInTx's reads of a merge that left
// nothing to settle: the resolver and the pre-rollback capture both find no
// conflict, and the FK repair finds no violation.
func expectCleanSettleReads(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT `table`, num_conflicts FROM dolt_conflicts").WillReturnRows(conflictRows(""))
	mock.ExpectQuery("SELECT `table`, num_conflicts FROM dolt_conflicts").WillReturnRows(conflictRows(""))
	mock.ExpectQuery("SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0").
		WillReturnRows(sqlmock.NewRows([]string{"table"}))
}

// expectDeclinedSettle expects settleMergeInTx to find a conflict in table,
// which the resolver declines (no table outside its allowlist is settled),
// capture it before the rollback (bd-578h9.15), and roll back.
func expectDeclinedSettle(mock sqlmock.Sqlmock, table string) {
	mock.ExpectQuery("SELECT `table`, num_conflicts FROM dolt_conflicts").WillReturnRows(conflictRows(table))
	mock.ExpectQuery("SELECT `table`, num_conflicts FROM dolt_conflicts").WillReturnRows(conflictRows(table))
	mock.ExpectRollback()
}

// TestCLIPullMergeRunsOnAPinnedLongTimeoutSession pins where the CLI pull
// route's merge runs (bd-e2q8zs): on its own session, which production opens
// with openLongTimeoutConn, moved onto the store's branch before it merges,
// and discarded afterwards. It never runs on a pool connection, whose per-I/O
// read deadline (10s by default) a large three-way merge can outlast, where
// the `dolt pull` it replaced had the CLI transfer timeout. The pool only
// answers which branch the store is on (pinStoreBranch). The merge runs in a
// transaction of its own with the allow-conflicts and force variables set,
// and settles there (settleMergeInTx): a clean merge commits, also with
// autocommit off, and a merge the resolver declines rolls back with its
// conflicts captured, so it is never published (GH#7839). The test also pins
// what the merge reports: Dolt's "Already up to date." error is no failure, a
// conflicted merge fails with a MergeConflictsError, and a failed merge or
// commit names the ref, a lost COMMIT response as ErrCommitIndeterminate.
func TestCLIPullMergeRunsOnAPinnedLongTimeoutSession(t *testing.T) {
	commitLost := errors.New("commit response lost")
	for _, tc := range []struct {
		name string
		// sessionBranch is the branch the fresh session starts on: a fresh
		// connection sits on the database's default branch.
		sessionBranch string
		// mergeErr, when non-nil, is returned by CALL DOLT_MERGE instead of a
		// row; conflicts builds the row otherwise.
		mergeErr  error
		conflicts int
		// commitErr, when non-nil, is returned by the transaction's COMMIT.
		commitErr error
		wantErr   string // empty: want nil
		// wantConflicts asks for a MergeConflictsError; wantIs for an error
		// errors.Is matches.
		wantConflicts bool
		wantIs        error
	}{
		{name: "fresh session moves to the store's branch", sessionBranch: "default"},
		{name: "session already on the store's branch", sessionBranch: "main"},
		{name: "already up to date is not an error", sessionBranch: "main", mergeErr: errors.New("Already up to date.")},
		{name: "a declined conflicted merge fails and rolls back", sessionBranch: "main", conflicts: 1,
			wantErr: "merge origin/main: conflicts found", wantConflicts: true},
		{name: "a failed merge names the ref and rolls back", sessionBranch: "main", mergeErr: errors.New("cannot merge"), wantErr: "merge origin/main: cannot merge"},
		{name: "a failed commit names the ref", sessionBranch: "main", commitErr: commitLost,
			wantErr: "merge origin/main: commit pull merge settlement: commit response lost", wantIs: storage.ErrCommitIndeterminate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, poolMock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = pool.Close() })
			session := cliMergeSession(t)

			poolMock.ExpectQuery("SELECT active_branch()").WillReturnRows(activeBranchRow("main"))
			session.ExpectQuery("SELECT active_branch()").WillReturnRows(activeBranchRow(tc.sessionBranch))
			if tc.sessionBranch != "main" {
				session.ExpectExec("CALL DOLT_CHECKOUT(?)").WithArgs("main").WillReturnResult(sqlmock.NewResult(0, 0))
			}
			session.ExpectExec("SET @@dolt_allow_commit_conflicts = 1").WillReturnResult(sqlmock.NewResult(0, 0))
			session.ExpectExec("SET @@dolt_force_transaction_commit = 1").WillReturnResult(sqlmock.NewResult(0, 0))
			session.ExpectBegin()
			merge := session.ExpectQuery("CALL DOLT_MERGE(?)").WithArgs("origin/main")
			switch {
			case tc.mergeErr != nil:
				merge.WillReturnError(tc.mergeErr)
				expectCleanSettleReads(session)
				if tc.wantErr != "" {
					session.ExpectRollback()
				} else {
					session.ExpectCommit()
				}
			case tc.conflicts != 0:
				merge.WillReturnRows(doltMergeRow(tc.conflicts))
				expectDeclinedSettle(session, "pull_msg_marker")
			default:
				merge.WillReturnRows(doltMergeRow(0))
				expectCleanSettleReads(session)
				commit := session.ExpectCommit()
				if tc.commitErr != nil {
					commit.WillReturnError(tc.commitErr)
				}
			}
			// The session is closed, not kept idle: the variables die with
			// it.
			session.ExpectClose()

			store := &DoltStore{db: pool, branch: "main"}
			err = store.mergeTrackingRefAfterCLIFetch(context.Background(), "origin/main")
			if tc.wantErr == "" && err != nil {
				t.Fatalf("merge: %v, want nil", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("merge: %v, want an error containing %q", err, tc.wantErr)
			}
			var mce *versioncontrolops.MergeConflictsError
			if got := errors.As(err, &mce); got != tc.wantConflicts {
				t.Errorf("merge error %v: errors.As MergeConflictsError = %v, want %v", err, got, tc.wantConflicts)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("merge error %v: want errors.Is %v", err, tc.wantIs)
			}
			if err := session.ExpectationsWereMet(); err != nil {
				t.Errorf("merge session statements: %v", err)
			}
			if err := poolMock.ExpectationsWereMet(); err != nil {
				t.Errorf("pool statements: %v", err)
			}
		})
	}

	// Production's session is a connection of its own, dialed from the
	// store's DSN as openLongTimeoutConn dials it, not one of the pool's: with
	// that DSN unroutable the merge fails acquiring it, before it asks the
	// pool, which expects nothing, anything.
	t.Run("production opens its own connection", func(t *testing.T) {
		pool, poolMock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = pool.Close() })
		store := &DoltStore{db: pool, branch: "main", connStr: unroutableConnStr}
		err = store.mergeTrackingRefAfterCLIFetch(context.Background(), "origin/main")
		if err == nil || !strings.Contains(err.Error(), "merge origin/main: acquire long-timeout connection") {
			t.Fatalf("merge with an unroutable DSN = %v, want it to fail acquiring its long-timeout connection", err)
		}
		if err := poolMock.ExpectationsWereMet(); err != nil {
			t.Errorf("pool statements: %v", err)
		}
	})
}
