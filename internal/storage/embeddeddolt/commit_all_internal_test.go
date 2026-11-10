//go:build cgo

package embeddeddolt

import (
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
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
