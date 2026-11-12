package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
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

// TestCLIPullMergeRunsOnAPinnedLongTimeoutSession pins where the CLI pull
// route's merge runs (bd-e2q8zs): on its own session, which production opens
// with openLongTimeoutConn, moved onto the store's branch before it merges,
// and discarded afterwards. It never runs on a pool connection, whose per-I/O
// read deadline (10s by default) a large three-way merge can outlast, where
// the `dolt pull` it replaced had the CLI transfer timeout. The pool only
// answers which branch the store is on (pinStoreBranch). The merge runs in a
// transaction of its own, committed unless the merge failed, as `dolt pull`
// sent COMMIT, so a conflicted merge persists with autocommit off too. The
// test also pins what the merge reports: Dolt's "Already up to date." error
// is no failure, a conflicted merge fails, and a failed merge or commit names
// the ref.
func TestCLIPullMergeRunsOnAPinnedLongTimeoutSession(t *testing.T) {
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
	}{
		{name: "fresh session moves to the store's branch", sessionBranch: "default"},
		{name: "session already on the store's branch", sessionBranch: "main"},
		{name: "already up to date is not an error", sessionBranch: "main", mergeErr: errors.New("Already up to date.")},
		{name: "a conflicted merge fails", sessionBranch: "main", conflicts: 1, wantErr: "merge origin/main: conflicts found"},
		{name: "a failed merge names the ref", sessionBranch: "main", mergeErr: errors.New("cannot merge"), wantErr: "merge origin/main: cannot merge"},
		{name: "a failed commit names the ref", sessionBranch: "main", commitErr: errors.New("commit refused"), wantErr: "merge origin/main: commit: commit refused"},
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
			session.ExpectExec("SET @@dolt_force_transaction_commit = 1").WillReturnResult(sqlmock.NewResult(0, 0))
			session.ExpectBegin()
			merge := session.ExpectQuery("CALL DOLT_MERGE(?)").WithArgs("origin/main")
			if tc.mergeErr != nil {
				merge.WillReturnError(tc.mergeErr)
				session.ExpectRollback()
			} else {
				merge.WillReturnRows(doltMergeRow(tc.conflicts))
				commit := session.ExpectCommit()
				if tc.commitErr != nil {
					commit.WillReturnError(tc.commitErr)
				}
			}
			// The session is closed, not kept idle: the force variable dies
			// with it.
			session.ExpectClose()

			store := &DoltStore{db: pool, branch: "main"}
			err = store.mergeTrackingRefAfterCLIFetch(context.Background(), "origin/main")
			if tc.wantErr == "" && err != nil {
				t.Fatalf("merge: %v, want nil", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("merge: %v, want an error containing %q", err, tc.wantErr)
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
