package dolt

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
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
//
// The doltAddAndCommit tests here stage metadata, a table outside the issue
// closure, so the call they pin is a one-table DOLT_ADD; TestWithIssueClosure
// and the publication-ordering tests cover the closure.
func TestDoltAddAndCommitDrainsCallResultSets(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
		WithArgs("metadata").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
		WithArgs("bd: test commit", " <>").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}))

	store := &DoltStore{db: db}

	if err := store.doltAddAndCommit(context.Background(), []string{"metadata"}, "bd: test commit"); err != nil {
		t.Fatalf("doltAddAndCommit: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestDoltAddAndCommitPostSQLFailuresAreIndeterminate(t *testing.T) {
	for _, tc := range []struct {
		name        string
		setup       func(sqlmock.Sqlmock)
		closeDB     bool
		cause       error
		mysqlNumber uint16
		wantContext string
	}{
		{
			name:        "connection acquisition",
			setup:       func(sqlmock.Sqlmock) {},
			closeDB:     true,
			wantContext: "acquire connection after SQL mutation",
		},
		{
			name: "DOLT_ADD",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
					WithArgs("metadata").
					WillReturnError(testConnectionLoss)
			},
			cause:       testConnectionLoss,
			wantContext: "dolt add metadata after SQL mutation",
		},
		{
			name: "untyped DOLT_COMMIT",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
					WithArgs("metadata").
					WillReturnRows(sqlmock.NewRows([]string{"status"}))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
					WithArgs("bd: test commit", " <>").
					WillReturnError(testConnectionLoss)
			},
			cause:       testConnectionLoss,
			wantContext: "dolt commit after SQL mutation",
		},
		{
			name: "typed deadlock DOLT_COMMIT",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
					WithArgs("metadata").
					WillReturnRows(sqlmock.NewRows([]string{"status"}))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
					WithArgs("bd: test commit", " <>").
					WillReturnError(&mysql.MySQLError{Number: 1213, Message: "deadlock"})
			},
			mysqlNumber: 1213,
			wantContext: "dolt commit after SQL mutation",
		},
		{
			name: "typed lock wait DOLT_COMMIT",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
					WithArgs("metadata").
					WillReturnRows(sqlmock.NewRows([]string{"status"}))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
					WithArgs("bd: test commit", " <>").
					WillReturnError(&mysql.MySQLError{Number: 1205, Message: "lock wait timeout"})
			},
			mysqlNumber: 1205,
			wantContext: "dolt commit after SQL mutation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			tc.setup(mock)
			store := &DoltStore{db: db}
			if tc.closeDB {
				mock.ExpectClose()
				if err := db.Close(); err != nil {
					t.Fatalf("close database: %v", err)
				}
			}

			err = store.doltAddAndCommit(context.Background(), []string{"metadata"}, "bd: test commit")
			if !errors.Is(err, storage.ErrCommitIndeterminate) {
				t.Fatalf("doltAddAndCommit() error = %v, want ErrCommitIndeterminate", err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Errorf("doltAddAndCommit() error = %v, want cause %v", err, tc.cause)
			}
			if tc.mysqlNumber != 0 {
				var mysqlErr *mysql.MySQLError
				if !errors.As(err, &mysqlErr) || mysqlErr.Number != tc.mysqlNumber {
					t.Errorf("doltAddAndCommit() error = %v, want MySQL %d", err, tc.mysqlNumber)
				}
			}
			if !strings.Contains(err.Error(), tc.wantContext) {
				t.Errorf("doltAddAndCommit() error = %q, want context %q", err, tc.wantContext)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations: %v", err)
			}
		})
	}
}

func TestDoltAddAndCommitAmbiguousConnectionFailuresTripCircuitOnce(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for range circuitFailureThreshold {
		mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
			WithArgs("metadata").
			WillReturnError(testConnectionLoss)
	}

	breaker := newTestCircuitBreaker(t)
	store := &DoltStore{db: db, breaker: breaker}
	for i := 0; i < circuitFailureThreshold; i++ {
		err := store.doltAddAndCommit(context.Background(), []string{"metadata"}, "bd: test commit")
		if !errors.Is(err, ErrCommitIndeterminate) {
			t.Fatalf("attempt %d error = %v, want ErrCommitIndeterminate", i+1, err)
		}
		if i < circuitFailureThreshold-1 && breaker.State() != circuitClosed {
			t.Fatalf("circuit state after %d failures = %q, want %q", i+1, breaker.State(), circuitClosed)
		}
	}
	if state := breaker.State(); state != circuitOpen {
		t.Fatalf("circuit state after %d ambiguous publication failures = %q, want %q", circuitFailureThreshold, state, circuitOpen)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestDoltAddAndCommitTreatsNothingToCommitAsSuccess(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
		WithArgs("metadata").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
		WithArgs("bd: test commit", " <>").
		WillReturnError(errors.New("nothing to commit"))

	store := &DoltStore{db: db}
	if err := store.doltAddAndCommit(context.Background(), []string{"metadata"}, "bd: test commit"); err != nil {
		t.Fatalf("doltAddAndCommit() error = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestCommitWithConfigCommitResponseLossIsIndeterminate(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-Am', ?, '--author', ?)")).
		WithArgs("bd: test config commit", " <>").
		WillReturnError(testConnectionLoss)

	store := &DoltStore{db: db}
	err = store.CommitWithConfig(context.Background(), "bd: test config commit")
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("CommitWithConfig() error = %v, want ErrCommitIndeterminate", err)
	}
	if !errors.Is(err, testConnectionLoss) {
		t.Fatalf("CommitWithConfig() error = %v, want cause %v", err, testConnectionLoss)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// expectClosureStaging expects what commitWorkingSet sends after a dolt_status
// read that named issues, on a database that holds every table of the issue
// closure: the SHOW TABLES read, then one DOLT_ADD naming the whole closure.
// It returns that DOLT_ADD's expectation for the caller to answer.
func expectClosureStaging(mock sqlmock.Sqlmock) *sqlmock.ExpectedQuery {
	held := sqlmock.NewRows([]string{"Tables_in_beads"}).AddRow("config").AddRow("metadata")
	for _, table := range wantIssueClosure {
		held.AddRow(table)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES")).WillReturnRows(held)
	return expectDoltAddOf(mock, wantIssueClosure...)
}

// expectDoltAddOf expects one CALL DOLT_ADD naming tables, in order.
func expectDoltAddOf(mock sqlmock.Sqlmock, tables ...string) *sqlmock.ExpectedQuery {
	stmt, args := callNamingTables("CALL DOLT_ADD", tables)
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg
	}
	return mock.ExpectQuery(regexp.QuoteMeta(stmt)).WithArgs(values...)
}

// TestCommitWorkingSetStagesTheHeldIssueClosure pins commitWorkingSet's
// widening: a dolt_status list that names a table of the issue closure stages
// every closure table the database holds, in one DOLT_ADD with the rest of the
// list, so a write that commits in SQL after the status read is staged whole;
// a database a migration has not reached stages the closure tables it has; a
// list with no closure table, or config under Commit, is staged as before; and
// a failed table listing stages nothing.
func TestCommitWorkingSetStagesTheHeldIssueClosure(t *testing.T) {
	// Migration 0063 creates provenance_events and 0074 id_tombstones.
	below0063 := []string{"child_counters", "comments", "compaction_snapshots", "dependencies",
		"issue_snapshots", "issues", "labels"}
	listErr := errors.New("show tables failed")
	for _, tc := range []struct {
		name    string
		dirty   []string
		held    []string // nil: no SHOW TABLES read expected
		listErr error
		staged  []string // nil: no DOLT_ADD expected
	}{
		{name: "issues on a migrated database", dirty: []string{"issues", "metadata", "config"},
			held: append([]string{"config", "metadata", "repo_mtimes"}, wantIssueClosure...),
			staged: []string{"child_counters", "comments", "compaction_snapshots", "dependencies", "id_tombstones",
				"issue_snapshots", "issues", "labels", "metadata", "provenance_events"}},
		{name: "labels on a database below migration 0063", dirty: []string{"labels"},
			held: append([]string{"metadata"}, below0063...), staged: below0063},
		{name: "no closure table", dirty: []string{"metadata", "config"}, staged: []string{"metadata"}},
		{name: "the table listing fails", dirty: []string{"issues"}, held: []string{}, listErr: listErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			status := sqlmock.NewRows([]string{"table_name"})
			for _, table := range tc.dirty {
				status.AddRow(table)
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT s.table_name FROM dolt_status")).WillReturnRows(status)
			if tc.held != nil {
				listing := mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES"))
				if tc.listErr != nil {
					listing.WillReturnError(tc.listErr)
				} else {
					held := sqlmock.NewRows([]string{"Tables_in_beads"})
					for _, table := range tc.held {
						held.AddRow(table)
					}
					listing.WillReturnRows(held)
				}
			}
			if tc.staged != nil {
				expectDoltAddOf(mock, tc.staged...).WillReturnRows(sqlmock.NewRows([]string{"status"}))
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
					WithArgs("bd: test commit", " <>").
					WillReturnRows(sqlmock.NewRows([]string{"hash"}))
			}

			store := &DoltStore{db: db}
			err = store.Commit(context.Background(), "bd: test commit")
			if tc.listErr != nil {
				if !errors.Is(err, tc.listErr) {
					t.Fatalf("Commit() error = %v, want the listing's %v", err, tc.listErr)
				}
			} else if err != nil {
				t.Fatalf("Commit() error = %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations: %v", err)
			}
		})
	}
}

func TestCommitCommitResponseLossIsIndeterminate(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("SELECT s.table_name FROM dolt_status")).
		WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("issues"))
	expectClosureStaging(mock).WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
		WithArgs("bd: test commit", " <>").
		WillReturnError(testConnectionLoss)

	store := &DoltStore{db: db}
	err = store.Commit(context.Background(), "bd: test commit")
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("Commit() error = %v, want ErrCommitIndeterminate", err)
	}
	if !errors.Is(err, testConnectionLoss) {
		t.Fatalf("Commit() error = %v, want cause %v", err, testConnectionLoss)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestCommitPropagatesDoltAddFailureBeforeCommit(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	stageErr := errors.New("stage issues failed")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT s.table_name FROM dolt_status")).
		WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("issues"))
	expectClosureStaging(mock).WillReturnError(stageErr)

	store := &DoltStore{db: db}
	err = store.Commit(context.Background(), "bd: test commit")
	if !errors.Is(err, stageErr) {
		t.Fatalf("Commit() error = %v, want staging cause %v", err, stageErr)
	}
	if want := "failed to stage " + strings.Join(wantIssueClosure, ", ") + " before commit"; !strings.Contains(err.Error(), want) {
		t.Fatalf("Commit() error = %q, want staging table context %q", err, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestPublicCommitAmbiguousConnectionFailuresTripCircuit(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	tests := []struct {
		name   string
		expect func(sqlmock.Sqlmock)
		commit func(*DoltStore) error
	}{
		{
			name: "Commit",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT s.table_name FROM dolt_status")).
					WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("issues"))
				expectClosureStaging(mock).WillReturnRows(sqlmock.NewRows([]string{"status"}))
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
					WithArgs("bd: test commit", " <>").
					WillReturnError(testConnectionLoss)
			},
			commit: func(store *DoltStore) error {
				return store.Commit(context.Background(), "bd: test commit")
			},
		},
		{
			name: "CommitMergeResolution",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT s.table_name FROM dolt_status")).
					WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("issues"))
				expectClosureStaging(mock).WillReturnRows(sqlmock.NewRows([]string{"status"}))
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
					WithArgs("bd: test merge commit", " <>").
					WillReturnError(testConnectionLoss)
			},
			commit: func(store *DoltStore) error {
				return store.CommitMergeResolution(context.Background(), "bd: test merge commit")
			},
		},
		{
			name: "CommitWithConfig",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-Am', ?, '--author', ?)")).
					WithArgs("bd: test config commit", " <>").
					WillReturnError(testConnectionLoss)
			},
			commit: func(store *DoltStore) error {
				return store.CommitWithConfig(context.Background(), "bd: test config commit")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			for range circuitFailureThreshold {
				tt.expect(mock)
			}

			breaker := newTestCircuitBreaker(t)
			store := &DoltStore{db: db, breaker: breaker}
			for i := 0; i < circuitFailureThreshold; i++ {
				err := tt.commit(store)
				if !errors.Is(err, ErrCommitIndeterminate) {
					t.Fatalf("attempt %d error = %v, want ErrCommitIndeterminate", i+1, err)
				}
				if i < circuitFailureThreshold-1 && breaker.State() != circuitClosed {
					t.Fatalf("circuit state after %d failures = %q, want %q", i+1, breaker.State(), circuitClosed)
				}
			}
			if state := breaker.State(); state != circuitOpen {
				t.Fatalf("circuit state after %d ambiguous commit failures = %q, want %q", circuitFailureThreshold, state, circuitOpen)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations: %v", err)
			}
		})
	}
}
