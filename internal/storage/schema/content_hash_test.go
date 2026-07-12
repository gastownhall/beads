package schema

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestEnsureContentHashColumnAddsWhenMissing verifies the idempotent upgrade adds
// the content_hash column to an existing cursor table that lacks it.
func TestEnsureContentHashColumnAddsWhenMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations LIKE 'content_hash'`).
		WillReturnRows(showColumnsRows())
	mock.ExpectExec(`ALTER TABLE schema_migrations ADD COLUMN content_hash CHAR\(64\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	added, err := mainSource.ensureContentHashColumn(context.Background(), db)
	if err != nil {
		t.Fatalf("ensureContentHashColumn: %v", err)
	}
	if !added {
		t.Fatal("ensureContentHashColumn added = false, want true when the column was missing")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestEnsureContentHashColumnNoOpWhenPresent verifies it issues no ALTER when the
// column already exists.
func TestEnsureContentHashColumnNoOpWhenPresent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations LIKE 'content_hash'`).
		WillReturnRows(showColumnsRows("content_hash"))
	// No ExpectExec: an ALTER here would be an unexpected call.

	added, err := mainSource.ensureContentHashColumn(context.Background(), db)
	if err != nil {
		t.Fatalf("ensureContentHashColumn: %v", err)
	}
	if added {
		t.Fatal("ensureContentHashColumn added = true, want false when the column already exists")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestAllMigrationsSQLRecordsContentHashes applies the full migration bundle
// through the dolt CLI and verifies every recorded migration carries the SHA-256
// of its migration file content (gastownhall/beads#4259 reporter fix No.2).
func TestAllMigrationsSQLRecordsContentHashes(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := filepath.Join(t.TempDir(), "hash-bundle")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create bundle dir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	got := map[string]string{}
	for _, r := range queryDoltCSV(t, dir, `SELECT version, content_hash FROM schema_migrations`) {
		got[r["version"]] = r["content_hash"]
	}

	for _, mf := range mainSource.list() {
		data, err := mainSource.files.ReadFile(mainSource.dir + "/" + mf.name)
		if err != nil {
			t.Fatalf("read migration %s: %v", mf.name, err)
		}
		sum := sha256.Sum256(data)
		want := hex.EncodeToString(sum[:])
		if h := got[strconv.Itoa(mf.version)]; h != want {
			t.Errorf("version %d content_hash = %q, want %q (sha256 of %s)", mf.version, h, want, mf.name)
		}
	}
}

// TestMigrationWorkNeededAddsContentHashColumnOnUpToDateDB is the regression for
// the silent no-op (gastownhall/beads#4259 reporter fix No.2 review): a database
// already at the latest migration version that predates the content_hash column
// must still report work needed, so MigrateUp runs migrate()'s idempotent ALTER
// instead of short-circuiting and leaving the recording/detection surface
// uninstalled.
func TestMigrationWorkNeededAddsContentHashColumnOnUpToDateDB(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Both sources are already at their latest version...
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	// ...but schema_migrations predates the content_hash column.
	mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations LIKE 'content_hash'`).
		WillReturnRows(showColumnsRows())

	needed, err := migrationWorkNeeded(context.Background(), db)
	if err != nil {
		t.Fatalf("migrationWorkNeeded: %v", err)
	}
	if !needed {
		t.Fatal("migrationWorkNeeded = false, want true when the content_hash column is missing")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestMigrationWorkNotNeededWhenContentHashColumnsPresent locks the probe's
// ordering: an up-to-date database that already has the content_hash columns and
// needs no backfill must not report work, so MigrateUp does not re-run the ALTER
// (or stage/commit) on every open.
func TestMigrationWorkNotNeededWhenContentHashColumnsPresent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	mock.ExpectQuery(`SHOW COLUMNS FROM schema_migrations LIKE 'content_hash'`).
		WillReturnRows(showColumnsRows("content_hash"))
	mock.ExpectQuery(`SHOW COLUMNS FROM ignored_schema_migrations LIKE 'content_hash'`).
		WillReturnRows(showColumnsRows("content_hash"))
	// No backfill pending (custom tables already populated).
	expectScalar(mock, "SELECT COUNT(*) FROM custom_types", "count", 1)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_statuses", "count", 1)

	needed, err := migrationWorkNeeded(context.Background(), db)
	if err != nil {
		t.Fatalf("migrationWorkNeeded: %v", err)
	}
	if needed {
		t.Fatal("migrationWorkNeeded = true, want false when columns are present and no backfill is pending")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
