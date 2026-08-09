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

func newTransactionPhaseFixture(t *testing.T) (*DoltStore, *sql.Conn, *doltTransaction, sqlmock.Sqlmock, sqlmock.Sqlmock) {
	t.Helper()
	ctx := context.Background()

	regularDB, regularMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new regular sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = regularDB.Close() })
	regularConn, err := regularDB.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire regular sqlmock connection: %v", err)
	}
	t.Cleanup(func() { _ = regularConn.Close() })
	regularMock.ExpectBegin()
	regularTx, err := regularConn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin regular sqlmock transaction: %v", err)
	}

	ignoredDB, ignoredMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new ignored sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = ignoredDB.Close() })
	ignoredMock.ExpectBegin()
	ignoredTx, err := ignoredDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin ignored sqlmock transaction: %v", err)
	}

	return &DoltStore{}, regularConn, &doltTransaction{
		regularTx: regularTx,
		ignoredTx: ignoredTx,
	}, regularMock, ignoredMock
}

func requireTransactionPhaseMocks(t *testing.T, regularMock, ignoredMock sqlmock.Sqlmock) {
	t.Helper()
	if err := regularMock.ExpectationsWereMet(); err != nil {
		t.Errorf("regular SQL expectations: %v", err)
	}
	if err := ignoredMock.ExpectationsWereMet(); err != nil {
		t.Errorf("ignored SQL expectations: %v", err)
	}
}

func TestRunInTransactionStageFailureAfterRegularCommitIsIndeterminateAndNotReplayed(t *testing.T) {
	store, conn, tx, regularMock, ignoredMock := newTransactionPhaseFixture(t)
	tx.dirty.MarkDirty("issues")
	regularMock.ExpectCommit()
	regularMock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM dolt_status s").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	regularMock.ExpectExec("CALL DOLT_ADD\\(\\?\\)").WithArgs("issues").
		WillReturnError(errors.New("invalid connection"))
	ignoredMock.ExpectRollback()

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
	requireTransactionPhaseMocks(t, regularMock, ignoredMock)
}

func TestRunInTransactionRegularPacketSyncCommitIsIndeterminateAndNotReplayed(t *testing.T) {
	store, conn, tx, regularMock, ignoredMock := newTransactionPhaseFixture(t)
	regularMock.ExpectCommit().WillReturnError(mysql.ErrPktSync)
	ignoredMock.ExpectRollback()

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
	requireTransactionPhaseMocks(t, regularMock, ignoredMock)
}
