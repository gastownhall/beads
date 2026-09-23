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

// A ref reaches DOLT_CLONE as --ref, verbatim, for both ref shapes.
func TestDoltCloneWithRef(t *testing.T) {
	for _, ref := range gitDataRefShapes {
		t.Run(ref, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CLONE('--ref', ?, ?, ?)")).
				WithArgs(ref, "git+https://example.com/repo.git", "beads").
				WillReturnResult(sqlmock.NewResult(0, 1))

			if err := DoltCloneWithRef(context.Background(), db, "git+https://example.com/repo.git", "beads", "", ref); err != nil {
				t.Fatalf("DoltCloneWithRef: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDoltCloneWithUserAndRef(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CLONE('--user', ?, '--ref', ?, ?, ?)")).
		WithArgs("alice", "refs/dolt/units/team-12542", "git+https://example.com/repo.git", "beads").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := DoltCloneWithRef(context.Background(), db, "git+https://example.com/repo.git", "beads", "alice", "refs/dolt/units/team-12542"); err != nil {
		t.Fatalf("DoltCloneWithRef: %v", err)
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
