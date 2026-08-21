package dolt

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

func TestRestoreDatabaseRoutesBackupURLWithoutStat(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const source = "s3://backup-bucket/beads"
	mock.ExpectExec("CALL DOLT_BACKUP('restore', ?, ?)").
		WithArgs(source, "beads").
		WillReturnResult(sqlmock.NewResult(0, 0))

	store := &DoltStore{database: "beads"}
	err = store.restoreDatabase(t.Context(), source, false, func(time.Duration) (*sql.DB, error) {
		return db, nil
	})
	if err != nil {
		t.Fatalf("RestoreDatabase: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestRestoreDatabaseKeepsDirectorySourceBehavior(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	source := t.TempDir()
	want, err := versioncontrolops.DirToFileURL(source)
	if err != nil {
		t.Fatalf("DirToFileURL(%q): %v", source, err)
	}
	mock.ExpectExec("CALL DOLT_BACKUP('restore', ?, ?)").
		WithArgs(want, "beads").
		WillReturnResult(sqlmock.NewResult(0, 0))

	store := &DoltStore{database: "beads"}
	err = store.restoreDatabase(t.Context(), source, false, func(time.Duration) (*sql.DB, error) {
		return db, nil
	})
	if err != nil {
		t.Fatalf("RestoreDatabase: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestRestoreDatabaseStatsDirectorySource(t *testing.T) {
	source := t.TempDir() + "/missing"
	store := &DoltStore{database: "beads"}
	err := store.restoreDatabase(t.Context(), source, false, func(time.Duration) (*sql.DB, error) {
		t.Fatal("RestoreDatabase opened a connection for a missing directory")
		return nil, nil
	})
	if err == nil {
		t.Fatal("RestoreDatabase returned nil for a missing directory")
	}
	if !strings.Contains(err.Error(), "backup source does not exist") {
		t.Fatalf("RestoreDatabase error %q does not report a missing backup source", err)
	}
}
