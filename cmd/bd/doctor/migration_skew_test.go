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

func expectRemoteMigrationShape(mock sqlmock.Sqlmock, ref string) {
	mock.ExpectQuery(`SHOW TABLES AS OF '` + ref + `' LIKE 'schema_migrations'`).
		WillReturnRows(sqlmock.NewRows([]string{"Tables_in_beads"}).AddRow("schema_migrations"))
	mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations AS OF '` + ref + `' LIKE 'content_hash'`).
		WillReturnRows(sqlmock.NewRows([]string{"Field"}).AddRow("content_hash"))
}

// expectLocalMigrationShape mocks the probes ReadMigrationContentHashes issues
// before its HEAD read (be-bv7x): a usable database, the cursor table, then
// its content_hash column.
func expectLocalMigrationShape(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM information_schema\.schemata WHERE schema_name = DATABASE\(\)`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`^SHOW TABLES$`).
		WillReturnRows(sqlmock.NewRows([]string{"Tables_in_beads"}).AddRow("schema_migrations"))
	mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations LIKE 'content_hash'`).
		WillReturnRows(sqlmock.NewRows([]string{"Field"}).AddRow("content_hash"))
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
	expectLocalMigrationShape(mock)
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a"))
	expectRemoteMigrationShape(mock, "remotes/upstream/main")
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
	expectLocalMigrationShape(mock)
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a"))
	// Real Dolt phrasing for an AS OF ref that is not cached locally.
	mock.ExpectQuery(`SHOW TABLES AS OF 'remotes/origin/main'`).
		WillReturnError(errors.New("branch not found: remotes/origin/main"))

	got := checkMigrationContentSkew(context.Background(), db, "origin")
	if got.Status != StatusOK {
		t.Errorf("status = %q, want %q (%s)", got.Status, StatusOK, got.Message)
	}
}

func TestCheckMigrationContentSkew_CachedRemotePredatesContentHashes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectRemoteAndBranch(mock, "main")
	expectLocalMigrationShape(mock)
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a"))
	mock.ExpectQuery(`SHOW TABLES AS OF 'remotes/origin/main' LIKE 'schema_migrations'`).
		WillReturnRows(sqlmock.NewRows([]string{"Tables_in_beads"}).AddRow("schema_migrations"))
	mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations AS OF 'remotes/origin/main' LIKE 'content_hash'`).
		WillReturnRows(sqlmock.NewRows([]string{"Field"}))

	got := checkMigrationContentSkew(context.Background(), db, "origin")
	if got.Status != StatusOK {
		t.Errorf("status = %q, want %q (%s)", got.Status, StatusOK, got.Message)
	}
	if !strings.Contains(got.Message, "predates migration content hashes") {
		t.Errorf("message = %q, want an old-cached-ref skip", got.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
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
	expectLocalMigrationShape(mock)
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a"))
	expectRemoteMigrationShape(mock, "remotes/origin/main")
	mock.ExpectQuery(`AS OF 'remotes/origin/main'`).
		WillReturnError(errors.New(`unbound variable "v1" in query`))

	got := checkMigrationContentSkew(context.Background(), db, "origin")
	if got.Status != StatusWarning {
		t.Fatalf("status = %q, want %q (%s)", got.Status, StatusWarning, got.Message)
	}
	// A failed local read warns with the same "Could not check" prefix, so
	// the stage has to name the remote read for the warning to be this one.
	if !strings.Contains(got.Message, `Could not check migration content skew (read schema_migrations at "remotes/origin/main")`) {
		t.Errorf("message = %q, want a 'Could not check' diagnostic from the remote read", got.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCheckMigrationContentSkew_NoLocalHashes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectRemoteAndBranch(mock, "main")
	expectLocalMigrationShape(mock)
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
	expectLocalMigrationShape(mock)
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a").AddRow(2, "b"))
	expectRemoteMigrationShape(mock, "remotes/origin/main")
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
	expectLocalMigrationShape(mock)
	mock.ExpectQuery(`SELECT version, content_hash FROM schema_migrations$`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "content_hash"}).AddRow(1, "a").AddRow(2, "b"))
	expectRemoteMigrationShape(mock, "remotes/origin/main")
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
