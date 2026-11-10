//go:build cgo

package embeddeddolt

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

func TestDoltCommitResponseLossIsIndeterminate(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	responseLoss := errors.New("connection lost before DOLT_COMMIT response")
	mock.ExpectExec("CALL DOLT_COMMIT('-Am', ?)").
		WithArgs("test: publish working set").
		WillReturnError(responseLoss)

	committed, err := commitAllInTx(t.Context(), tx, "test: publish working set", true)
	if committed {
		t.Fatal("commitAllInTx() committed = true after response loss, want false")
	}
	if !errors.Is(err, responseLoss) {
		t.Fatalf("commitAllInTx() error = %v, want cause %v", err, responseLoss)
	}
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("commitAllInTx() error = %v, want ErrCommitIndeterminate", err)
	}

	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// TestCommitVerificationPreservesTransactionOutcome drives commitAllThrough
// on sqlmock: when a required read after DOLT_COMMIT fails, the transaction
// still commits and the result is a *storage.CommitVerificationError that
// keeps DOLT_COMMIT's own outcome, never a claim that the mutation failed or
// a no-op a caller would tolerate. A failed optional diagnostic read after the
// residual tables are known still yields the residual report (GH#5433).
func TestCommitVerificationPreservesTransactionOutcome(t *testing.T) {
	const msg = "test: publish working set"
	cause := errors.New("verification read failed")
	nothing := errors.New("nothing to commit, working tree clean")

	expectCommit := func(mock sqlmock.Sqlmock, committed bool) {
		exec := mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-Am', ?)")).WithArgs(msg)
		if committed {
			exec.WillReturnResult(sqlmock.NewResult(0, 1))
		} else {
			exec.WillReturnError(nothing)
		}
	}
	const status = `SELECT table_name FROM dolt_status`
	statusRows := func(tables ...any) *sqlmock.Rows {
		rows := sqlmock.NewRows([]string{"table_name"})
		for _, table := range tables {
			rows.AddRow(table)
		}
		return rows
	}
	failures := map[string]struct {
		operation string
		expect    func(sqlmock.Sqlmock)
	}{
		"status query": {"read dolt_status", func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(status).WillReturnError(cause)
		}},
		"status scan": {"read dolt_status", func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(status).WillReturnRows(statusRows(nil))
		}},
		"status iteration": {"read dolt_status", func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(status).WillReturnRows(statusRows("issues").RowError(0, cause))
		}},
		"ignore policy": {"read bd-owned ignore policy", func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(status).WillReturnRows(statusRows("issues"))
			mock.ExpectQuery(`information_schema\.schemata`).WillReturnError(cause)
		}},
	}

	for name, failure := range failures {
		for _, committed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/committed=%v", name, committed), func(t *testing.T) {
				db, mock := newCommitAllMock(t)
				mock.ExpectBegin()
				expectCommit(mock, committed)
				failure.expect(mock)
				mock.ExpectCommit()

				gotCommitted, err := commitAllThrough(t.Context(), sqlmockWithTx(t, db), msg, true)
				if gotCommitted != committed {
					t.Errorf("committed = %v, want %v", gotCommitted, committed)
				}
				var verr *storage.CommitVerificationError
				if !errors.As(err, &verr) {
					t.Fatalf("error = %v, want *storage.CommitVerificationError", err)
				}
				wantOutcome, wantText := storage.CommitOutcomeNothingToCommit,
					"nothing to commit; working-set cleanliness could not be verified"
				if committed {
					wantOutcome, wantText = storage.CommitOutcomeSucceeded,
						"commit may have landed, but completeness could not be verified"
				}
				if verr.Outcome != wantOutcome || verr.Operation != failure.operation {
					t.Errorf("payload = {%q, %q}, want {%q, %q}", verr.Outcome, verr.Operation, wantOutcome, failure.operation)
				}
				if !errors.Is(err, storage.ErrCommitVerificationFailed) {
					t.Errorf("error = %v, want errors.Is ErrCommitVerificationFailed", err)
				}
				// A NULL scan fails inside database/sql, not with cause.
				if name != "status scan" && !errors.Is(err, cause) {
					t.Errorf("error = %v, want errors.Is the read's cause", err)
				}
				if !strings.Contains(err.Error(), wantText) {
					t.Errorf("error = %q, want it to contain %q", err, wantText)
				}
				if errors.Is(err, storage.ErrCommitIndeterminate) {
					t.Errorf("error = %v claims the commit itself is indeterminate", err)
				}
				if issueops.IsNothingToCommitError(err) {
					t.Errorf("error = %v is classified as a tolerable no-op", err)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet sqlmock expectations (transaction not published?): %v", err)
				}
			})
		}
	}

	t.Run("optional pattern read failure keeps the residual", func(t *testing.T) {
		db, mock := newCommitAllMock(t)
		mock.ExpectBegin()
		expectCommit(mock, false)
		mock.ExpectQuery(status).WillReturnRows(statusRows("issues"))
		mock.ExpectQuery(`information_schema\.schemata`).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT pattern FROM dolt_ignore")).WillReturnError(cause)
		mock.ExpectCommit()

		_, err := commitAllThrough(t.Context(), sqlmockWithTx(t, db), msg, true)
		var residual *storage.UncommittedChangesError
		if !errors.As(err, &residual) {
			t.Fatalf("error = %v, want *storage.UncommittedChangesError", err)
		}
		if !slices.Equal(residual.Tables, []string{"issues"}) || residual.IgnorePatterns != nil {
			t.Errorf("residual = %+v, want tables [issues] and no patterns", residual)
		}
		if errors.Is(err, storage.ErrCommitVerificationFailed) {
			t.Errorf("error = %v, want the residual report, not a verification failure", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})
}

func newCommitAllMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// sqlmockWithTx mirrors withConn's transaction handling: the callback's error
// rolls back, success publishes through commitEmbeddedTx.
func sqlmockWithTx(t *testing.T, db *sql.DB) func(func(*sql.Tx) error) error {
	return func(fn func(*sql.Tx) error) error {
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			_ = tx.Rollback()
			return err
		}
		return commitEmbeddedTx(tx)
	}
}
