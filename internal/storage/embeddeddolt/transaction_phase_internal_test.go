//go:build cgo

package embeddeddolt

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
)

func TestTransactionCleanupAfterSQLCommitIsIndeterminate(t *testing.T) {
	cleanupErr := errors.New("connection cleanup failed")
	err := joinTransactionCleanupError(nil, cleanupErr, true)
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("error = %v, want cleanup cause", err)
	}
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("error = %v, want ErrCommitIndeterminate", err)
	}
}

func TestTransactionCleanupBeforeSQLCommitRemainsDefinite(t *testing.T) {
	callbackErr := errors.New("callback failed")
	cleanupErr := errors.New("connection cleanup failed")
	err := joinTransactionCleanupError(callbackErr, cleanupErr, false)
	if !errors.Is(err, callbackErr) {
		t.Fatalf("error = %v, want callback cause", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("error = %v, want cleanup cause", err)
	}
	if errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("error = %v must remain definite before SQL commit", err)
	}
}

func TestEmbeddedSQLCommitResponseLossIsIndeterminate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	commitLoss := errors.New("i/o timeout")
	mock.ExpectCommit().WillReturnError(commitLoss)

	err = commitEmbeddedTx(tx)
	if !errors.Is(err, commitLoss) {
		t.Fatalf("commitEmbeddedTx() error = %v, want cause %v", err, commitLoss)
	}
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("commitEmbeddedTx() error = %v, want ErrCommitIndeterminate", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}
