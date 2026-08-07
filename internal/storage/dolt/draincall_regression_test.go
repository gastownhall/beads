package dolt

import (
	"context"
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
					WithArgs("issues").
					WillReturnError(testConnectionLoss)
			},
			cause:       testConnectionLoss,
			wantContext: "dolt add issues after SQL mutation",
		},
		{
			name: "untyped DOLT_COMMIT",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
					WithArgs("issues").
					WillReturnRows(sqlmock.NewRows([]string{"status"}))
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
					WithArgs("issues").
					WillReturnRows(sqlmock.NewRows([]string{"status"}))
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
					WithArgs("issues").
					WillReturnRows(sqlmock.NewRows([]string{"status"}))
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

			err = store.doltAddAndCommit(context.Background(), []string{"issues"}, "bd: test commit")
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
			WithArgs("issues").
			WillReturnError(testConnectionLoss)
	}

	breaker := newTestCircuitBreaker(t)
	store := &DoltStore{db: db, breaker: breaker}
	for i := 0; i < circuitFailureThreshold; i++ {
		err := store.doltAddAndCommit(context.Background(), []string{"issues"}, "bd: test commit")
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
		WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
		WithArgs("bd: test commit", " <>").
		WillReturnError(errors.New("nothing to commit"))

	store := &DoltStore{db: db}
	if err := store.doltAddAndCommit(context.Background(), []string{"issues"}, "bd: test commit"); err != nil {
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

func TestCommitCommitResponseLossIsIndeterminate(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("SELECT s.table_name FROM dolt_status")).
		WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("issues"))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
		WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
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
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
		WithArgs("issues").
		WillReturnError(stageErr)

	store := &DoltStore{db: db}
	err = store.Commit(context.Background(), "bd: test commit")
	if !errors.Is(err, stageErr) {
		t.Fatalf("Commit() error = %v, want staging cause %v", err, stageErr)
	}
	if !strings.Contains(err.Error(), "stage issues") {
		t.Fatalf("Commit() error = %q, want staging table context", err)
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
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
					WithArgs("issues").
					WillReturnRows(sqlmock.NewRows([]string{"status"}))
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
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
					WithArgs("issues").
					WillReturnRows(sqlmock.NewRows([]string{"status"}))
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

func TestDoltAddAndCommitInTxClassifiesCommitResponses(t *testing.T) {
	for _, tc := range []struct {
		name              string
		commitErr         error
		wantIndeterminate bool
	}{
		{
			name:              "untyped response loss",
			commitErr:         testConnectionLoss,
			wantIndeterminate: true,
		},
		{
			name:      "typed rollback response",
			commitErr: &mysql.MySQLError{Number: 1213, Message: "deadlock"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			mock.ExpectBegin()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("begin transaction: %v", err)
			}
			mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
				WithArgs("issues").
				WillReturnRows(sqlmock.NewRows([]string{"status"}))
			mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
				WithArgs("bd: test commit", " <>").
				WillReturnError(tc.commitErr)
			mock.ExpectRollback()

			store := &DoltStore{}
			err = store.doltAddAndCommitInTx(context.Background(), tx, []string{"issues"}, "bd: test commit")
			if got := errors.Is(err, storage.ErrCommitIndeterminate); got != tc.wantIndeterminate {
				t.Fatalf("doltAddAndCommitInTx() error = %v, ErrCommitIndeterminate = %v, want %v", err, got, tc.wantIndeterminate)
			}
			if !errors.Is(err, tc.commitErr) {
				t.Fatalf("doltAddAndCommitInTx() error = %v, want cause %v", err, tc.commitErr)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatalf("rollback transaction: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations: %v", err)
			}
		})
	}
}

func TestDoltAddAndCommitInTxStopsAtStageFailure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	stageErr := errors.New("stage failed")
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
		WithArgs("issues").
		WillReturnError(stageErr)
	mock.ExpectRollback()

	store := &DoltStore{}
	err = store.doltAddAndCommitInTx(context.Background(), tx, []string{"issues", "events"}, "bd: test commit")
	if !errors.Is(err, stageErr) {
		t.Fatalf("doltAddAndCommitInTx() error = %v, want stage failure %v", err, stageErr)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback transaction: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}
