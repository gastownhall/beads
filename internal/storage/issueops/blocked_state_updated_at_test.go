package issueops

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// utcTimeArg is a sqlmock.Argument matcher that asserts the bound value is a
// time.Time with Location() == time.UTC. The fix (GH#4298) uses time.Now().UTC()
// which always has Location()==time.UTC. A regression to bare time.Now() produces
// Location()==time.Local — a distinct *time.Location even on a UTC-configured
// machine — so this matcher deterministically catches both a removed updated_at
// and a revert to local time.Now(), independent of CI timezone.
type utcTimeArg struct{}

func (utcTimeArg) Match(v driver.Value) bool {
	t, ok := v.(time.Time)
	return ok && t.Location() == time.UTC
}

func TestRecomputeIsBlockedSetsUpdatedAtUTC(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	// mark template: SET i.is_blocked = 1, i.updated_at = ?
	mock.ExpectExec(`SET i\.is_blocked = 1, i\.updated_at = \?`).
		WithArgs(utcTimeArg{}, "bd-aaaa").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// unmark template: SET i.is_blocked = 0, i.updated_at = ?
	mock.ExpectExec(`SET i\.is_blocked = 0, i\.updated_at = \?`).
		WithArgs(utcTimeArg{}, "bd-aaaa").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := recomputeIsBlockedPassForIssuesInTx(context.Background(), tx, []string{"bd-aaaa"}); err != nil {
		t.Fatalf("recomputeIsBlockedPassForIssuesInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

func TestMarkIsBlockedSetsUpdatedAtUTC(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	// mark template: SET i.is_blocked = 1, i.updated_at = ?
	mock.ExpectExec(`SET i\.is_blocked = 1, i\.updated_at = \?`).
		WithArgs(utcTimeArg{}, "bd-aaaa").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := markIsBlockedPassForIssuesInTx(context.Background(), tx, []string{"bd-aaaa"}); err != nil {
		t.Fatalf("markIsBlockedPassForIssuesInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

func TestMarkDirectBlockingDependencySetsUpdatedAtUTC(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	// UPDATE issues s SET s.is_blocked = 1, s.updated_at = ? WHERE s.id = ? ...
	mock.ExpectExec(`SET s\.is_blocked = 1, s\.updated_at = \?`).
		WithArgs(utcTimeArg{}, "bd-src", "bd-tgt").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := markDirectBlockingDependencySourceInTx(context.Background(), tx, "bd-src", false, "bd-tgt", DepTargetIssue); err != nil {
		t.Fatalf("markDirectBlockingDependencySourceInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}

func TestMarkDirectBlockingDependencyWispSetsUpdatedAtUTC(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	// UPDATE wisps s SET s.is_blocked = 1, s.updated_at = ? WHERE s.id = ? ...
	mock.ExpectExec(`SET s\.is_blocked = 1, s\.updated_at = \?`).
		WithArgs(utcTimeArg{}, "bd-src", "bd-tgt").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := markDirectBlockingDependencySourceInTx(context.Background(), tx, "bd-src", true, "bd-tgt", DepTargetIssue); err != nil {
		t.Fatalf("markDirectBlockingDependencySourceInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations not met: %v", err)
	}
}
