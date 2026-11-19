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

// cliMergeSettleReads expects the settle pass's reads on the merge session's
// transaction: the resolver's and the pre-rollback capture's dolt_conflicts
// queries, and — when the conflicts are empty — the FK-violation probe that
// precedes the commit. declinedTable, when non-empty, is the conflicted
// table the resolver declines, and the transaction rolls back instead.
func cliMergeSettleReads(session sqlmock.Sqlmock, declinedTable string) {
	conflictRows := func() *sqlmock.Rows {
		rows := sqlmock.NewRows([]string{"table", "num_conflicts"})
		if declinedTable != "" {
			rows.AddRow(declinedTable, 1)
		}
		return rows
	}
	session.ExpectQuery("SELECT `table`, num_conflicts FROM dolt_conflicts").WillReturnRows(conflictRows())
	session.ExpectQuery("SELECT `table`, num_conflicts FROM dolt_conflicts").WillReturnRows(conflictRows())
	if declinedTable == "" {
		session.ExpectQuery("SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0").
			WillReturnRows(sqlmock.NewRows([]string{"table"}))
	}
}

// TestCLIPullMergeRunsOnAPinnedLongTimeoutSession pins where the CLI pull
// route's merge runs (bd-e2q8zs): on its own session, which production opens
// with openLongTimeoutConn, moved onto the store's branch before it merges,
// and discarded afterwards. It never runs on a pool connection, whose per-I/O
// read deadline (10s by default) a large three-way merge can outlast, where
// the `dolt pull` it replaced had the CLI transfer timeout. The pool only
// answers which branch the store is on (pinStoreBranch). The merge runs and
// settles in a transaction of its own (settleMergeInTx, GH#7839): a clean or
// accepted merge commits there, a declined conflict rolls the merge back with
// its conflicts captured, and a lost COMMIT response is indeterminate rather
// than a silent failure. The test also pins what the merge reports: Dolt's
// "Already up to date." error is no failure, a conflicted merge fails, and a
// failed merge or commit names the ref.
func TestCLIPullMergeRunsOnAPinnedLongTimeoutSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		// sessionBranch is the branch the fresh session starts on: a fresh
		// connection sits on the database's default branch.
		sessionBranch string
		// mergeErr, when non-nil, is returned by CALL DOLT_MERGE instead of a
		// row; declinedTable builds the conflicted row otherwise.
		mergeErr          error
		declinedTable     string
		wantErr           string // empty: want nil
		wantIndeterminate bool
	}{
		{name: "fresh session moves to the store's branch", sessionBranch: "default"},
		{name: "session already on the store's branch", sessionBranch: "main"},
		{name: "already up to date is not an error", sessionBranch: "main", mergeErr: errors.New("Already up to date.")},
		{name: "a conflicted merge fails and rolls back", sessionBranch: "main", declinedTable: "pull_msg_marker", wantErr: "merge origin/main: conflicts found"},
		{name: "a failed merge names the ref", sessionBranch: "main", mergeErr: errors.New("cannot merge"), wantErr: "merge origin/main: cannot merge"},
		{name: "a failed commit names the ref", sessionBranch: "main", wantErr: "merge origin/main: commit pull merge settlement: commit refused", wantIndeterminate: true},
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
			session.ExpectExec("SET @@dolt_allow_commit_conflicts = 1").WillReturnResult(sqlmock.NewResult(0, 0))
			session.ExpectBegin()
			merge := session.ExpectQuery("CALL DOLT_MERGE(?)").WithArgs("origin/main")
			if tc.mergeErr != nil {
				if strings.Contains(tc.mergeErr.Error(), "up to date") {
					// The squash path's no-op arrives as an error and is
					// settled as a clean merge: nothing to resolve, commit.
					merge.WillReturnError(tc.mergeErr)
					cliMergeSettleReads(session, "")
					session.ExpectCommit()
				} else {
					merge.WillReturnError(tc.mergeErr)
					// A non-conflict merge error still settles: no conflicts,
					// no violations, and the merge error surfaces on rollback.
					cliMergeSettleReads(session, "")
					session.ExpectRollback()
				}
			} else {
				merge.WillReturnRows(doltMergeRow(0))
				cliMergeSettleReads(session, tc.declinedTable)
				if tc.declinedTable != "" {
					session.ExpectRollback()
				} else if tc.wantIndeterminate {
					session.ExpectCommit().WillReturnError(errors.New("commit refused"))
				} else {
					session.ExpectCommit()
				}
			}
			// The session is closed, not kept idle: both session flags die
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
			if tc.wantIndeterminate && !errors.Is(err, storage.ErrCommitIndeterminate) {
				t.Fatalf("merge: %v, want storage.ErrCommitIndeterminate", err)
			}
			if tc.declinedTable != "" {
				var mce *versioncontrolops.MergeConflictsError
				if !errors.As(err, &mce) || !conflictsNameTable(mce.Conflicts, tc.declinedTable) {
					t.Fatalf("merge: %v, want a MergeConflictsError naming %s", err, tc.declinedTable)
				}
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
