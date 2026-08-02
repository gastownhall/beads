package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCheckMigrationContentSkew_NoDatabase(t *testing.T) {
	got := CheckMigrationContentSkew(&SharedStore{}) // Store() == nil
	if got.Status != StatusOK {
		t.Errorf("status = %q, want %q", got.Status, StatusOK)
	}
}

func expectRemoteAndBranch(mock sqlmock.Sqlmock, branch string) {
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes WHERE name = \?`).
		WithArgs("origin").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(`SELECT active_branch\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"b"}).AddRow(branch))
}

func TestCheckMigrationContentSkew_NoRemote(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// The configured sync remote is absent from dolt_remotes -> skip.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes WHERE name = \?`).
		WithArgs("origin").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))

	got := checkMigrationContentSkew(context.Background(), db, "origin")
	if got.Status != StatusOK {
		t.Errorf("status = %q, want %q (%s)", got.Status, StatusOK, got.Message)
	}
	if !strings.Contains(got.Message, "not configured") {
		t.Errorf("message = %q, want a 'not configured' skip", got.Message)
	}
}

// The check must compare against the CONFIGURED sync remote, not whichever
// remote sorts first in dolt_remotes (bd-6dnrw.27).
func TestCheckMigrationContentSkew_UsesConfiguredRemote(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM dolt_remotes WHERE name = \?`).
		WithArgs("upstream").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(`SELECT active_branch\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"b"}).AddRow("main"))
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a"))
	mock.ExpectQuery(`AS OF 'remotes/upstream/main'`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a"))

	got := checkMigrationContentSkew(context.Background(), db, "upstream")
	if got.Status != StatusOK {
		t.Errorf("status = %q, want %q (%s)", got.Status, StatusOK, got.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCheckMigrationContentSkew_NoCachedRemoteRef(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectRemoteAndBranch(mock, "main")
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a"))
	// Real Dolt phrasing for an AS OF ref that is not cached locally.
	mock.ExpectQuery(`AS OF 'remotes/origin/main'`).
		WillReturnError(errors.New("branch not found: remotes/origin/main"))

	got := checkMigrationContentSkew(context.Background(), db, "origin")
	if got.Status != StatusOK {
		t.Errorf("status = %q, want %q (%s)", got.Status, StatusOK, got.Message)
	}
}

// An UNEXPECTED failure of the remote-side read must surface as a warning, not
// be swallowed as OK — the original #4270 bug hid `unbound variable "v1" in
// query` (Dolt rejecting bind params in AS OF) behind a green check forever.
func TestCheckMigrationContentSkew_UnexpectedErrorWarns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectRemoteAndBranch(mock, "main")
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a"))
	mock.ExpectQuery(`AS OF 'remotes/origin/main'`).
		WillReturnError(errors.New(`unbound variable "v1" in query`))

	got := checkMigrationContentSkew(context.Background(), db, "origin")
	if got.Status != StatusWarning {
		t.Fatalf("status = %q, want %q (%s)", got.Status, StatusWarning, got.Message)
	}
	if !strings.Contains(got.Message, "Could not check") {
		t.Errorf("message = %q, want a 'Could not check' diagnostic", got.Message)
	}
}

func TestCheckMigrationContentSkew_NoLocalHashes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectRemoteAndBranch(mock, "main")
	// Old database: rows exist but content_hash is all NULL.
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, nil))

	got := checkMigrationContentSkew(context.Background(), db, "origin")
	if got.Status != StatusOK {
		t.Errorf("status = %q, want %q (%s)", got.Status, StatusOK, got.Message)
	}
	if !strings.Contains(got.Message, "No local migration content hashes") {
		t.Errorf("message = %q, want a no-local-hashes skip", got.Message)
	}
}

func TestCheckMigrationContentSkew_Matches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectRemoteAndBranch(mock, "main")
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a").AddRow(2, "b"))
	mock.ExpectQuery(`AS OF 'remotes/origin/main'`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a").AddRow(2, "b"))

	got := checkMigrationContentSkew(context.Background(), db, "origin")
	if got.Status != StatusOK {
		t.Errorf("status = %q, want %q (%s)", got.Status, StatusOK, got.Message)
	}
}

func TestCheckMigrationContentSkew_Diverges(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectRemoteAndBranch(mock, "main")
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a").AddRow(2, "b"))
	mock.ExpectQuery(`AS OF 'remotes/origin/main'`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a").AddRow(2, "DIFFERENT"))

	got := checkMigrationContentSkew(context.Background(), db, "origin")
	if got.Status != StatusWarning {
		t.Fatalf("status = %q, want %q (%s)", got.Status, StatusWarning, got.Message)
	}
	if !strings.Contains(got.Message, "0002") {
		t.Errorf("message = %q, want it to name migration 0002", got.Message)
	}
}
