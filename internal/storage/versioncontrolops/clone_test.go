package versioncontrolops

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDoltCloneWithoutUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CLONE(?, ?)")).
		WithArgs("https://example.com/repo", "beads").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := DoltClone(context.Background(), db, "https://example.com/repo", "beads", ""); err != nil {
		t.Fatalf("DoltClone: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDoltCloneWithUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CLONE('--user', ?, ?, ?)")).
		WithArgs("alice", "https://example.com/repo", "beads").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := DoltClone(context.Background(), db, "https://example.com/repo", "beads", "alice"); err != nil {
		t.Fatalf("DoltClone: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
