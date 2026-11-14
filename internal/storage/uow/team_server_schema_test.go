package uow

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/cenkalti/backoff/v4"
	mysql "github.com/go-sql-driver/mysql"

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

// expectExcursionSelection scripts the successful target selection an
// excursion identity check performs: the USE of the target database and the
// session-database assertion that must precede the unqualified identity read.
func expectExcursionSelection(mock sqlmock.Sqlmock, database string) {
	mock.ExpectExec(regexp.QuoteMeta("USE `" + database + "`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow(database))
}

// expectExcursionIdentityQuery scripts the _project_id read an excursion
// identity check issues after selecting the target, returning projectID.
func expectExcursionIdentityQuery(mock sqlmock.Sqlmock, projectID string) {
	expectProjectIDQuery(mock, projectID)
}

// The bd-rgvivq core (gastownhall/beads#5572): a plain proxied open that an
// explicit --database pointed at another project's beads database must be
// refused with the mismatch naming the workspace identity, the requested
// database, and the stored identity. No schema-version probe is scripted: a
// stored identity with no migration history behind it is still compared.
func TestCheckExcursionIdentity_ForeignProjectID_Refuses(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	expectExcursionSelection(mock, "beads_other")
	expectExcursionIdentityQuery(mock, "project-BBBB")

	err := checkExcursionIdentity(context.Background(), db, "beads_other", "project-AAAA")
	if err == nil {
		t.Fatal("checkExcursionIdentity = nil, want refusal when the excursion target serves a different project")
	}
	for _, want := range []string{"PROJECT IDENTITY MISMATCH", "project-AAAA", "project-BBBB", "beads_other"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
	// The plain-mode remedy: the override is the thing to undo, and the
	// explicit bypass is named. Team-server provisioning advice (bts init,
	// re-adopting with bd init --team-server) does not apply to a plain
	// workspace's excursion and must not be recommended.
	for _, notWant := range []string{"bts init", "bd init --team-server"} {
		if strings.Contains(err.Error(), notWant) {
			t.Errorf("error %q must not recommend the team-server remedy %q", err, notWant)
		}
	}
	if !strings.Contains(err.Error(), "BEADS_SKIP_IDENTITY_CHECK=1") {
		t.Errorf("error %q should name the explicit bypass", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// An excursion check with no expectation (the ordinary plain open, whose
// topology resolver cleared the identity) must not query at all — asserting
// anything there would break plain co-tenant workspaces on their own
// configured database.
func TestCheckExcursionIdentity_NoExpectation_SkipsWithoutQuerying(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()

	if err := checkExcursionIdentity(context.Background(), db, "beads_other", ""); err != nil {
		t.Fatalf("checkExcursionIdentity = %v, want nil with no expectation", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("an open with no expectation must not query the database: %v", err)
	}
}

// A session the server left on a database other than the requested one is a
// misrouted front door, not a foreign identity: the identity read must not
// run against the wrong database (see assertSessionDatabase).
func TestCheckExcursionIdentity_MisdirectedSession_Refuses(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	mock.ExpectExec(regexp.QuoteMeta("USE `beads_other`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow("beads_gateway_default"))

	err := checkExcursionIdentity(context.Background(), db, "beads_other", "project-AAAA")
	if err == nil {
		t.Fatal("checkExcursionIdentity = nil, want the misdirected session reported")
	}
	if !strings.Contains(err.Error(), "beads_gateway_default") {
		t.Errorf("error %q should name the database the session actually landed on", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// A database the server says does not exist has no identity to compare: the
// check skips and the open path below decides (the migrating open creates
// it; a preview open refuses with its own remedy).
func TestCheckExcursionIdentity_DatabaseNotFound_Skips(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	mock.ExpectExec(regexp.QuoteMeta("USE `beads_absent`")).
		WillReturnError(&mysql.MySQLError{Number: 1049, Message: "unknown database"})

	if err := checkExcursionIdentity(context.Background(), db, "beads_absent", "project-AAAA"); err != nil {
		t.Fatalf("checkExcursionIdentity = %v, want nil on a database-not-found selection", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no further SQL may run after a database-not-found selection: %v", err)
	}
}

// A denial hides database existence, so it must not be read as absence: the
// open fails with the classified denial instead of skipping the check.
func TestCheckExcursionIdentity_AccessDenied_Fails(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	mock.ExpectExec(regexp.QuoteMeta("USE `beads_ungranted`")).
		WillReturnError(&mysql.MySQLError{Number: 1044, Message: "access denied"})

	err := checkExcursionIdentity(context.Background(), db, "beads_ungranted", "project-AAAA")
	if err == nil {
		t.Fatal("checkExcursionIdentity = nil, want the denial surfaced")
	}
	if !strings.Contains(err.Error(), "access to database \"beads_ungranted\" was denied") {
		t.Errorf("error %q should classify the denial, not guess absence", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// A read error that is not the missing-metadata-table case is a real fault
// and must fail the open rather than pass as "no identity available".
func TestCheckExcursionIdentity_ReadError_Surfaces(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	expectExcursionSelection(mock, "beads_other")
	mock.ExpectQuery("SELECT value FROM metadata WHERE .key. = \\?").
		WithArgs("_project_id").
		WillReturnError(errors.New("Error 1105 (HY000): connection reset"))

	err := checkExcursionIdentity(context.Background(), db, "beads_other", "project-AAAA")
	if err == nil {
		t.Fatal("checkExcursionIdentity = nil, want the read error surfaced")
	}
	if !strings.Contains(err.Error(), "beads_other") {
		t.Errorf("error %q should name the database", err)
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("error %q should keep the read error reachable", err)
	}
}

// A table-not-exist error naming a table OTHER than metadata is not the
// non-beads-database case the check tolerates: it fails closed.
func TestCheckExcursionIdentity_MissingOtherTable_FailsClosed(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	expectExcursionSelection(mock, "beads_other")
	mock.ExpectQuery("SELECT value FROM metadata WHERE .key. = \\?").
		WithArgs("_project_id").
		WillReturnError(errors.New("Error 1146 (42S02): table 'beads_other.widgets' doesn't exist"))

	err := checkExcursionIdentity(context.Background(), db, "beads_other", "project-AAAA")
	if err == nil {
		t.Fatal("checkExcursionIdentity = nil, want a table-not-exist error naming another table to fail closed")
	}
}

// A serialization failure from the identity read must stay retryable: the
// helper returns it bare so initSchemaAttempt's classification can send the
// whole attempt through the existing backoff.
func TestCheckExcursionIdentity_SerializationError_StaysBare(t *testing.T) {
	mock, db, closeDB := newVersionMockDB(t)
	defer closeDB()
	expectExcursionSelection(mock, "beads_other")
	serializationErr := &mysql.MySQLError{Number: 1213, Message: "Deadlock found"}
	mock.ExpectQuery("SELECT value FROM metadata WHERE .key. = \\?").
		WithArgs("_project_id").
		WillReturnError(serializationErr)

	err := checkExcursionIdentity(context.Background(), db, "beads_other", "project-AAAA")
	if !errors.Is(err, serializationErr) {
		t.Fatalf("checkExcursionIdentity = %v (%T), want the bare serialization error", err, err)
	}
	var permanent *backoff.PermanentError
	if errors.As(err, &permanent) {
		t.Fatalf("checkExcursionIdentity must not classify a serialization failure as permanent: %v", err)
	}
}

// TestInitSchemaAttempt_ExcursionMismatchStopsBeforeOpen pins where the
// excursion identity refusal lands in the plain proxied open: on the pinned
// connection, after the team-server branch, before the preview split and
// before MigrateUpWithLock — so nothing that mutates the target (migration
// lock, CREATE DATABASE, migrations, version reconciliation) has run when a
// foreign identity is refused. Only the excursion's three statements are
// scripted; any later SQL would be unexpected to sqlmock.
func TestInitSchemaAttempt_ExcursionMismatchStopsBeforeOpen(t *testing.T) {
	for _, tt := range []struct {
		name    string
		preview bool
	}{
		{name: "ordinary open"},
		{name: "preview open", preview: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer db.Close()

			expectExcursionSelection(mock, "beads_other")
			expectExcursionIdentityQuery(mock, "project-BBBB")

			p := &doltSQLProvider{
				defaultBranch:     defaultBranch,
				db:                db,
				serverEndpoint:    "tcp:127.0.0.1:3306",
				expectedProjectID: "project-AAAA",
				preview:           tt.preview,
			}
			// initSchemaAttempt directly, not initSchema: backoff.Retry
			// unwraps *backoff.PermanentError before returning it, so the
			// classification is only observable on the attempt's own error.
			err = p.initSchemaAttempt(context.Background(), "beads_other", &bootstrapPreparer{provider: p, database: "beads_other"})
			if err == nil {
				t.Fatal("initSchemaAttempt() = nil, want the identity mismatch refusal")
			}
			if !strings.Contains(err.Error(), "PROJECT IDENTITY MISMATCH") {
				t.Fatalf("initSchemaAttempt() error = %v, want the identity mismatch refusal", err)
			}
			var permanent *backoff.PermanentError
			if !errors.As(err, &permanent) {
				t.Fatalf("initSchemaAttempt() error = %T (%v), want a permanent refusal: a retry would re-attempt the open it just refused", err, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

// A serialization failure during the excursion identity read keeps the
// provider's ordinary backoff: the retry attempt re-runs the identity check
// (which then passes on a matching identity) and proceeds to the converged
// open. The refusal path is not classified retryable anywhere.
func TestInitSchemaAttempt_ExcursionSerializationRetried(t *testing.T) {
	calls := stubConvergedMigrateUp(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	// Attempt 1: the identity read deadlocks.
	expectExcursionSelection(mock, "beads_other")
	mock.ExpectQuery("SELECT value FROM metadata WHERE .key. = \\?").
		WithArgs("_project_id").
		WillReturnError(&mysql.MySQLError{Number: 1213, Message: "Deadlock found"})
	// Attempt 2: the identity read matches; the converged open follows with
	// its single forward-drift version read.
	expectExcursionSelection(mock, "beads_other")
	expectExcursionIdentityQuery(mock, "project-AAAA")
	expectVersionQuery(mock, schema.LatestVersion())

	p := &doltSQLProvider{
		defaultBranch:     defaultBranch,
		db:                db,
		serverEndpoint:    "tcp:127.0.0.1:3306",
		expectedProjectID: "project-AAAA",
	}
	if err := p.initSchema(context.Background(), "beads_other"); err != nil {
		t.Fatalf("initSchema() error = %v, want the retried attempt to open", err)
	}
	if *calls != 1 {
		t.Errorf("MigrateUpWithLock called %d times, want 1", *calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
