package uow

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// expectCursorProbe mocks the information_schema existence probe that
// migrationSource.currentVersion issues before it ever reads the cursor
// table. The probe was added by be-bv7x so a not-yet-created cursor table
// never poisons the pooled Dolt session with a failing statement.
func expectCursorProbe(mock sqlmock.Sqlmock, table string, exists bool) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	rows := sqlmock.NewRows([]string{"Tables_in_beads"}).AddRow("issues")
	if exists {
		rows.AddRow(table)
	}
	mock.ExpectQuery("^SHOW TABLES$").WillReturnRows(rows)
}

func expectVersionQuery(mock sqlmock.Sqlmock, version int) {
	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(version))
}

func newVersionMockDB(t *testing.T) (sqlmock.Sqlmock, schema.DBConn, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	return mock, db, func() { _ = db.Close() }
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = origStderr
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return buf.String()
}

func TestCheckTeamServerSchema_FreshDB_RefusesWithBtsInit(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	expectVersionQuery(mock, 0)

	err := checkTeamServerSchema(context.Background(), db, "beads_team")
	if err == nil {
		t.Fatal("checkTeamServerSchema = nil, want refusal for empty (v0) database")
	}
	if !strings.Contains(err.Error(), "bts init") {
		t.Errorf("error %q should point at 'bts init'", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckTeamServerSchema_MissingTable_RefusesWithBtsInit(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	// With the be-bv7x probe in front, an absent cursor table is reported by
	// the probe returning 0 rather than by the cursor read erroring out.
	expectCursorProbe(mock, "schema_migrations", false)

	err := checkTeamServerSchema(context.Background(), db, "beads_team")
	if err == nil {
		t.Fatal("checkTeamServerSchema = nil, want refusal when schema_migrations is absent")
	}
	if !strings.Contains(err.Error(), "bts init") {
		t.Errorf("error %q should point at 'bts init'", err)
	}
}

func TestCheckTeamServerSchema_Behind_RefusesWithBtsMigrate(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	behind := schema.LatestVersion() - 1
	expectVersionQuery(mock, behind)

	err := checkTeamServerSchema(context.Background(), db, "beads_team")
	if err == nil {
		t.Fatal("checkTeamServerSchema = nil, want refusal for behind database")
	}
	if !strings.Contains(err.Error(), "bts migrate") {
		t.Errorf("error %q should point at 'bts migrate'", err)
	}
	if strings.Contains(err.Error(), "bd migrate schema") {
		t.Errorf("error %q must not suggest bd-driven migration on a bts-owned schema", err)
	}
}

func TestCheckTeamServerSchema_Equal_OK(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	expectVersionQuery(mock, schema.LatestVersion())

	if err := checkTeamServerSchema(context.Background(), db, "beads_team"); err != nil {
		t.Fatalf("checkTeamServerSchema = %v, want nil at matching version", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckTeamServerSchema_Ahead_ReturnsSchemaSkewError(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	ahead := schema.LatestVersion() + 2
	// The ahead case delegates to CheckForwardDrift, which re-reads the version.
	expectVersionQuery(mock, ahead)
	expectVersionQuery(mock, ahead)

	err := checkTeamServerSchema(context.Background(), db, "beads_team")
	if !schema.IsSchemaSkewError(err) {
		t.Fatalf("checkTeamServerSchema = %v (%T), want *schema.SchemaSkewError", err, err)
	}
}

func TestCheckTeamServerSchema_Behind_EscapeHatchDoesNotBypass(t *testing.T) {
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	expectVersionQuery(mock, schema.LatestVersion()-1)

	if err := checkTeamServerSchema(context.Background(), db, "beads_team"); err == nil {
		t.Fatal("checkTeamServerSchema = nil; BD_IGNORE_SCHEMA_SKEW must not allow writing against an older bts schema")
	}
}

func TestCheckTeamServerSchema_Ahead_EscapeHatchWarnsInsteadOfRefusing(t *testing.T) {
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	ahead := schema.LatestVersion() + 1
	expectVersionQuery(mock, ahead)
	expectVersionQuery(mock, ahead)

	var err error
	stderr := captureStderr(t, func() {
		err = checkTeamServerSchema(context.Background(), db, "beads_team")
	})
	if err != nil {
		t.Fatalf("checkTeamServerSchema = %v, want nil with BD_IGNORE_SCHEMA_SKEW=1", err)
	}
	if !strings.Contains(stderr, "schema skew ignored") {
		t.Errorf("stderr = %q, want a schema-skew warning", stderr)
	}
}

func expectProjectIDQuery(mock sqlmock.Sqlmock, projectID string) {
	mock.ExpectQuery("SELECT value FROM metadata WHERE .key. = \\?").
		WithArgs("_project_id").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(projectID))
}

// The negative control: a database provisioned for a different project must be
// refused, not adopted. This is the whole point of the guard — on the proxied
// path nothing else re-checks identity after init.
func TestCheckTeamServerIdentity_ForeignProjectID_Refuses(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	expectProjectIDQuery(mock, "project-BBBB")

	err := checkTeamServerIdentity(context.Background(), db, "beads_team", "project-AAAA")
	if err == nil {
		t.Fatal("checkTeamServerIdentity = nil, want refusal when the database serves a different project")
	}
	for _, want := range []string{"PROJECT IDENTITY MISMATCH", "project-AAAA", "project-BBBB", "beads_team"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
	if !strings.Contains(err.Error(), "bd init --team-server") {
		t.Errorf("error %q should tell the user re-running init is safe", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckTeamServerIdentity_MatchingProjectID_OK(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	expectProjectIDQuery(mock, "project-AAAA")

	if err := checkTeamServerIdentity(context.Background(), db, "beads_team", "project-AAAA"); err != nil {
		t.Fatalf("checkTeamServerIdentity = %v, want nil when identities match", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// `bd init --team-server` adopts the provisioned identity, so it passes an
// empty expectation. The check must then not query at all — asserting the
// locally-minted placeholder would reject every correct init.
func TestCheckTeamServerIdentity_AdoptionPath_SkipsWithoutQuerying(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()

	if err := checkTeamServerIdentity(context.Background(), db, "beads_team", ""); err != nil {
		t.Fatalf("checkTeamServerIdentity = %v, want nil on the adoption path", err)
	}
	// No ExpectQuery was registered; any query would be an unexpected call.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("adoption path must not query the database: %v", err)
	}
}

// A bts-managed database with no _project_id row is an invalid state, not a
// legacy one: adoptTeamServerIdentity refuses to attach to such a database in
// the first place. Soft-skipping it would let one deleted row disable the
// guard for every client.
func TestCheckTeamServerIdentity_DatabaseWithoutIdentity_Refuses(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	mock.ExpectQuery("SELECT value FROM metadata WHERE .key. = \\?").
		WithArgs("_project_id").
		WillReturnRows(sqlmock.NewRows([]string{"value"}))

	err := checkTeamServerIdentity(context.Background(), db, "beads_team", "project-AAAA")
	if err == nil {
		t.Fatal("checkTeamServerIdentity = nil, want refusal for a database with no _project_id")
	}
	if !strings.Contains(err.Error(), "bts init") {
		t.Errorf("error %q should point the operator at 'bts init'", err)
	}
}

func TestCheckTeamServerIdentity_EmptyStoredIdentity_Refuses(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	expectProjectIDQuery(mock, "")

	if err := checkTeamServerIdentity(context.Background(), db, "beads_team", "project-AAAA"); err == nil {
		t.Fatal("checkTeamServerIdentity = nil, want refusal for an empty stored identity")
	}
}

// Unlike the legacy-database cases above, a genuine read failure is surfaced:
// checkTeamServerSchema has already proven the schema is present, so the
// metadata table exists and an error here is a real fault.
func TestCheckTeamServerIdentity_ReadError_Surfaces(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	mock.ExpectQuery("SELECT value FROM metadata WHERE .key. = \\?").
		WithArgs("_project_id").
		WillReturnError(errors.New("Error 1105 (HY000): connection reset"))

	err := checkTeamServerIdentity(context.Background(), db, "beads_team", "project-AAAA")
	if err == nil {
		t.Fatal("checkTeamServerIdentity = nil, want the read error surfaced")
	}
	if !strings.Contains(err.Error(), "beads_team") {
		t.Errorf("error %q should name the database", err)
	}
}

// expectExcursionIdentityRead scripts checkExcursionIdentity's read sequence
// against an existing database that has a metadata table: existence probe,
// USE, session-database assertion, metadata-table probe, identity read.
func expectExcursionIdentityRead(mock sqlmock.Sqlmock, database, storedProjectID string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?")).
		WithArgs(database).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta("USE `" + database + "`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(database))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = 'metadata'")).
		WithArgs(database).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	expectProjectIDQuery(mock, storedProjectID)
}

func pinMockConn(t *testing.T) (sqlmock.Sqlmock, *sql.Conn) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return mock, conn
}

func assertExcursionMismatch(t *testing.T, err error, database, local, stored string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil, want PROJECT IDENTITY MISMATCH for %q", database)
	}
	for _, want := range []string{"PROJECT IDENTITY MISMATCH", local, stored, database, "BEADS_SKIP_IDENTITY_CHECK=1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
	// Plain mode has no team server: its remedies must not leak in.
	for _, unwanted := range []string{"bts ", "bd init --team-server"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("plain-mode refusal %q must not recommend %q", err, unwanted)
		}
	}
}

// gastownhall/beads#5572: an excursion that selects another beads project's
// database is refused, naming both identities and the database.
func TestCheckExcursionIdentity_ForeignProjectID_Refuses(t *testing.T) {
	mock, conn := pinMockConn(t)
	expectExcursionIdentityRead(mock, "beads_other", "project-BBBB")

	err := checkExcursionIdentity(context.Background(), conn, "beads_other", "project-AAAA")
	assertExcursionMismatch(t, err, "beads_other", "project-AAAA", "project-BBBB")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// The refusal must come before ANY open work on the target: no migration
// lock, CREATE, migration, or preview attach. sqlmock is ordered and holds
// only the identity reads, and the migrate seam fails the test if reached.
func TestInitSchemaAttempt_ExcursionMismatchStopsBeforeOpen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		preview bool
	}{
		{name: "ordinary", preview: false},
		{name: "preview", preview: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			migrateCalls := stubConvergedMigrateUp(t)
			sqlDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer func() { _ = sqlDB.Close() }()
			expectExcursionIdentityRead(mock, "beads_other", "project-BBBB")

			p := &doltSQLProvider{defaultBranch: defaultBranch, db: sqlDB, preview: tc.preview, expectedProjectID: "project-AAAA"}
			start := time.Now()
			err = p.initSchema(context.Background(), "beads_other")
			assertExcursionMismatch(t, err, "beads_other", "project-AAAA", "project-BBBB")
			// Permanent: the backoff loop must not retry a mismatch.
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("mismatch took %s; it must be a permanent (unretried) failure", elapsed)
			}
			if *migrateCalls != 0 {
				t.Errorf("migrateUpWithLock called %d times after a mismatch, want 0", *migrateCalls)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet or extra statements: %v", err)
			}
		})
	}
}

// BEADS_SKIP_IDENTITY_CHECK=1 is a plain-excursion hatch applied by the CLI
// topology; the team-server guard never reads it.
func TestCheckTeamServerIdentity_SkipIdentityEnvDoesNotBypass(t *testing.T) {
	t.Setenv("BEADS_SKIP_IDENTITY_CHECK", "1")
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	expectProjectIDQuery(mock, "project-BBBB")

	err := checkTeamServerIdentity(context.Background(), db, "beads_team", "project-AAAA")
	if err == nil || !strings.Contains(err.Error(), "PROJECT IDENTITY MISMATCH") {
		t.Fatalf("checkTeamServerIdentity = %v, want the team-server mismatch refusal", err)
	}
}
