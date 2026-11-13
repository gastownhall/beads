package dolt

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
)

func newTransactionPhaseFixture(t *testing.T) (*DoltStore, *sql.Conn, *doltTransaction, sqlmock.Sqlmock) {
	t.Helper()
	ctx := context.Background()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire sqlmock connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	mock.ExpectBegin()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin sqlmock transaction: %v", err)
	}

	return &DoltStore{}, conn, &doltTransaction{tx: tx}, mock
}

func requireTransactionPhaseMocks(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("SQL expectations: %v", err)
	}
}

func TestRunInTransactionStageFailureAfterCommitIsIndeterminateAndNotReplayed(t *testing.T) {
	store, conn, tx, mock := newTransactionPhaseFixture(t)
	tx.dirty.MarkDirty("issues")
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM dolt_status s").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("CALL DOLT_ADD\\(\\?\\)").WithArgs("issues").
		WillReturnError(errors.New("invalid connection"))

	callbackCalls := 0
	runnerCalls := 0
	err := store.runInTransaction(context.Background(), "test: phase boundary", func(storage.Transaction) error {
		callbackCalls++
		return nil
	}, func(ctx context.Context, commitMsg string, fn func(storage.Transaction) error) error {
		runnerCalls++
		if err := fn(tx); err != nil {
			return err
		}
		return store.finishDoltTransaction(ctx, conn, tx, commitMsg)
	})
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("error = %v, want ErrCommitIndeterminate", err)
	}
	if callbackCalls != 1 {
		t.Fatalf("callback calls = %d, want 1", callbackCalls)
	}
	if runnerCalls != 1 {
		t.Fatalf("transaction runner calls = %d, want 1", runnerCalls)
	}
	requireTransactionPhaseMocks(t, mock)
}

func TestRunInTransactionPacketSyncCommitIsIndeterminateAndNotReplayed(t *testing.T) {
	store, conn, tx, mock := newTransactionPhaseFixture(t)
	mock.ExpectCommit().WillReturnError(mysql.ErrPktSync)

	callbackCalls := 0
	runnerCalls := 0
	err := store.runInTransaction(context.Background(), "test: packet sync", func(storage.Transaction) error {
		callbackCalls++
		return nil
	}, func(ctx context.Context, commitMsg string, fn func(storage.Transaction) error) error {
		runnerCalls++
		if err := fn(tx); err != nil {
			return err
		}
		return store.finishDoltTransaction(ctx, conn, tx, commitMsg)
	})
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("error = %v, want ErrCommitIndeterminate", err)
	}
	if !errors.Is(err, mysql.ErrPktSync) {
		t.Fatalf("error = %v, want cause %v", err, mysql.ErrPktSync)
	}
	if callbackCalls != 1 {
		t.Fatalf("callback calls = %d, want 1", callbackCalls)
	}
	if runnerCalls != 1 {
		t.Fatalf("transaction runner calls = %d, want 1", runnerCalls)
	}
	requireTransactionPhaseMocks(t, mock)
}
