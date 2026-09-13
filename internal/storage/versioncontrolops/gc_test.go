package versioncontrolops

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDoltGCDisablesArchiveCompression(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_GC('--archive-level', '0')")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := DoltGC(context.Background(), db); err != nil {
		t.Fatalf("DoltGC: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
