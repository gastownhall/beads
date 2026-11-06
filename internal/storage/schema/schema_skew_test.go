package schema

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
)

// -- checkSchemaSkew unit tests (mock DB) --

func TestCheckSchemaSkew_FreshDB_NoError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(0))

	if err := checkSchemaSkew(context.Background(), db); err != nil {
		t.Fatalf("checkSchemaSkew = %v, want nil for fresh DB (version=0)", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckSchemaSkew_EqualVersion_NoError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(LatestVersion()))

	if err := checkSchemaSkew(context.Background(), db); err != nil {
		t.Fatalf("checkSchemaSkew = %v, want nil when DB at binary version", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckSchemaSkew_OneAhead_ReturnsSchemaSkewError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	dbVersion := LatestVersion() + 1
	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(dbVersion))

	got := checkSchemaSkew(context.Background(), db)
	if got == nil {
		t.Fatal("checkSchemaSkew = nil, want error when DB one migration ahead")
	}
	var skewErr *SchemaSkewError
	if !errors.As(got, &skewErr) {
		t.Fatalf("error type = %T (%v), want *SchemaSkewError", got, got)
	}
	if skewErr.DBVersion != dbVersion {
		t.Errorf("DBVersion = %d, want %d", skewErr.DBVersion, dbVersion)
	}
	if skewErr.BinaryVersion != LatestVersion() {
		t.Errorf("BinaryVersion = %d, want %d", skewErr.BinaryVersion, LatestVersion())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckSchemaSkew_ThreeAhead_ReturnsSchemaSkewError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	dbVersion := LatestVersion() + 3
	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(dbVersion))

	got := checkSchemaSkew(context.Background(), db)
	if got == nil {
		t.Fatal("checkSchemaSkew = nil, want error when DB three migrations ahead")
	}
	var skewErr *SchemaSkewError
	if !errors.As(got, &skewErr) {
		t.Fatalf("error type = %T (%v), want *SchemaSkewError", got, got)
	}
	if skewErr.DBVersion != dbVersion {
		t.Errorf("DBVersion = %d, want %d", skewErr.DBVersion, dbVersion)
	}
	if skewErr.BinaryVersion != LatestVersion() {
		t.Errorf("BinaryVersion = %d, want %d", skewErr.BinaryVersion, LatestVersion())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckSchemaSkew_EscapeHatch_ReturnsNilAndWarns(t *testing.T) {
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	dbVersion := LatestVersion() + 3
	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(dbVersion))

	// Capture stderr to verify warning is emitted.
	origStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stderr = w

	gotErr := checkSchemaSkew(context.Background(), db)

	_ = w.Close()
	os.Stderr = origStderr

	var buf bytes.Buffer
	if _, copyErr := io.Copy(&buf, r); copyErr != nil {
		t.Fatal(copyErr)
	}
	_ = r.Close()

	if gotErr != nil {
		t.Fatalf("checkSchemaSkew = %v, want nil when BD_IGNORE_SCHEMA_SKEW=1", gotErr)
	}

	wantWarning := fmt.Sprintf(
		"Warning: schema skew ignored — database (v%d) is ahead of binary (v%d); some queries may fail",
		dbVersion, LatestVersion(),
	)
	if !strings.Contains(buf.String(), wantWarning) {
		t.Errorf("stderr = %q\nwant to contain warning:\n  %q", buf.String(), wantWarning)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestCheckSchemaSkew_MissingTable_NoError covers the writable open path, where
// CheckForwardDrift runs before initSchema creates schema_migrations on a fresh
// database. A table-not-exist error must be treated as version 0 (no forward
// drift), not surfaced as a skew-check failure.
func TestCheckSchemaSkew_MissingTable_NoError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// With the be-bv7x probe in front, an absent cursor table is reported by
	// the probe returning 0 rather than by the cursor read erroring out.
	expectCursorProbe(mock, "schema_migrations", false)

	if err := checkSchemaSkew(context.Background(), db); err != nil {
		t.Fatalf("checkSchemaSkew = %v, want nil when schema_migrations is absent (fresh DB)", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestCheckForwardDrift_Conn_Ahead confirms the exported guard accepts a DBConn
// (so the embedded writable path can pass a pinned *sql.Conn) and still reports
// forward drift.
func TestCheckForwardDrift_Conn_Ahead(t *testing.T) {
	ctx := context.Background()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	defer conn.Close()

	dbVersion := LatestVersion() + 2
	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(dbVersion))

	got := CheckForwardDrift(ctx, conn)
	if !IsSchemaSkewError(got) {
		t.Fatalf("CheckForwardDrift = %v (%T), want *SchemaSkewError", got, got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// -- SchemaSkewError message copy tests --

func TestSchemaSkewError_Error_Singular(t *testing.T) {
	e := &SchemaSkewError{DBVersion: 43, BinaryVersion: 42}
	want := "schema version mismatch: database is at v43, binary knows up to v42 (1 migration ahead)"
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q\nwant:   %q", got, want)
	}
}

func TestSchemaSkewError_Error_Plural(t *testing.T) {
	e := &SchemaSkewError{DBVersion: 45, BinaryVersion: 42}
	want := "schema version mismatch: database is at v45, binary knows up to v42 (3 migrations ahead)"
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q\nwant:   %q", got, want)
	}
}

func TestSchemaSkewError_UserMessage_ExactCopy(t *testing.T) {
	e := &SchemaSkewError{DBVersion: 45, BinaryVersion: 42}
	want := "schema version mismatch: database is at v45, binary knows up to v42 (3 migrations ahead)\n" +
		"\n" +
		"  Your bd binary is stale. Queries for dropped or renamed columns will fail\n" +
		"  with cryptic SQL errors (e.g. \"column X could not be found in any table in scope\").\n" +
		"\n" +
		"  Rebuild from main:\n" +
		"    CGO_ENABLED=0 go build -tags gms_pure_go ./cmd/bd\n" +
		"\n" +
		"  Or install the latest release:\n" +
		"    CGO_ENABLED=0 go install -tags gms_pure_go github.com/steveyegge/beads/cmd/bd@latest\n" +
		"\n" +
		"  To proceed despite the risk (some read commands may still work):\n" +
		"    BD_IGNORE_SCHEMA_SKEW=1 bd <command>\n" +
		"    bd --ignore-schema-skew <command>\n"
	if got := e.UserMessage(); got != want {
		t.Errorf("UserMessage() mismatch.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestSchemaSkewError_EscapeHint_ExactCopy(t *testing.T) {
	e := &SchemaSkewError{DBVersion: 45, BinaryVersion: 42}
	want := "BD_IGNORE_SCHEMA_SKEW=1 bd <command>  or  bd --ignore-schema-skew <command>"
	if got := e.EscapeHint(); got != want {
		t.Errorf("EscapeHint() = %q\nwant:         %q", got, want)
	}
}

// -- IsSchemaSkewError tests --

func TestIsSchemaSkewError_DirectError(t *testing.T) {
	err := &SchemaSkewError{DBVersion: 45, BinaryVersion: 42}
	if !IsSchemaSkewError(err) {
		t.Error("IsSchemaSkewError(*SchemaSkewError) = false, want true")
	}
}

func TestIsSchemaSkewError_WrappedError(t *testing.T) {
	skew := &SchemaSkewError{DBVersion: 45, BinaryVersion: 42}
	wrapped := fmt.Errorf("schema version check: %w", skew)
	doubleWrapped := fmt.Errorf("failed to open database: %w", wrapped)
	if !IsSchemaSkewError(doubleWrapped) {
		t.Error("IsSchemaSkewError(wrapped *SchemaSkewError) = false, want true")
	}
}

func TestIsSchemaSkewError_OtherError(t *testing.T) {
	err := errors.New("some unrelated error")
	if IsSchemaSkewError(err) {
		t.Error("IsSchemaSkewError(non-SchemaSkewError) = true, want false")
	}
}

// -- CheckBehindDrift unit tests (mock DB) --

func TestCheckBehindDrift_FreshDB_ReturnsSchemaBehindError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// On a fresh database where schema_migrations does not exist, the cursor probe
	// returns 0 and no bare SELECT against schema_migrations is issued.
	expectCursorProbe(mock, "schema_migrations", false)

	got := CheckBehindDrift(context.Background(), db)
	if got == nil {
		t.Fatal("CheckBehindDrift = nil, want *SchemaBehindError for fresh DB (version=0)")
	}
	var behindErr *SchemaBehindError
	if !errors.As(got, &behindErr) {
		t.Fatalf("error type = %T (%v), want *SchemaBehindError", got, got)
	}
	if behindErr.DBVersion != 0 {
		t.Errorf("DBVersion = %d, want 0", behindErr.DBVersion)
	}
	if behindErr.BinaryVersion != LatestVersion() {
		t.Errorf("BinaryVersion = %d, want %d", behindErr.BinaryVersion, LatestVersion())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// -- CheckReadableDrift unit tests (mock DB) --

// readableColumns stands in for the projection a caller passes.
var readableColumns = []string{"id", "arrival_seq"}

func expectMainCursor(mock sqlmock.Sqlmock, version int) {
	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(version))
}

// expectIssuesColumns mocks missingColumns' read of issues: the table probe,
// then the SHOW COLUMNS listing with these names in its Field column.
func expectIssuesColumns(mock sqlmock.Sqlmock, columns ...string) {
	expectCursorProbe(mock, "issues", true)
	rows := sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"})
	for _, column := range columns {
		rows.AddRow(column, "bigint", "YES", "", nil, "")
	}
	mock.ExpectQuery(`^SHOW COLUMNS FROM issues$`).WillReturnRows(rows)
}

// expectLeasesColumns mocks the leases probe with exactly these columns
// present (the table itself always exists in this mock).
func expectLeasesColumns(mock sqlmock.Sqlmock, columns ...string) {
	expectCursorProbe(mock, "leases", true)
	rows := sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"})
	for _, column := range columns {
		rows.AddRow(column, "bigint", "YES", "", nil, "")
	}
	mock.ExpectQuery(`^SHOW COLUMNS FROM leases$`).WillReturnRows(rows)
}

// issueOnlyRequirements is the single-requirement shape the table tests
// below use; the shared projection's full shape is
// IssueReadableRequirements, covered by its own cases and the sqlbuild
// parity test.
func issueOnlyRequirements(columns []string) []ReadableRequirement {
	return []ReadableRequirement{{Table: "issues", Columns: columns}}
}

func TestCheckReadableDrift(t *testing.T) {
	behind := LatestVersion() - 1
	for _, tc := range []struct {
		name   string
		expect func(sqlmock.Sqlmock)
		// fullProjection selects IssueReadableRequirements' two-table shape
		// over the single-issue-requirement stand-in.
		fullProjection bool
		// check judges the returned error.
		check func(t *testing.T, err error)
	}{
		{
			// A database at the binary's version costs nothing beyond the
			// version read: no column listing is expected.
			name:   "at latest",
			expect: func(mock sqlmock.Sqlmock) { expectMainCursor(mock, LatestVersion()) },
			check:  requireNoDriftError,
		},
		{
			name:   "ahead is refused as forward drift",
			expect: func(mock sqlmock.Sqlmock) { expectMainCursor(mock, LatestVersion()+1) },
			check: func(t *testing.T, err error) {
				if !IsSchemaSkewError(err) {
					t.Fatalf("CheckReadableDrift = %T (%v), want *SchemaSkewError", err, err)
				}
			},
		},
		{
			// bd-578h9.5 / #5920: a behind database that still has every
			// column the reads select keeps serving them. Column names
			// compare case-insensitively.
			name: "behind with every column stays readable",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectIssuesColumns(mock, "id", "title", "ARRIVAL_SEQ")
			},
			check: requireNoDriftError,
		},
		{
			name: "behind and missing a column is refused",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectIssuesColumns(mock, "id", "title")
			},
			check: requireBehindError(behind),
		},
		{
			// No issues table at all: every column is missing. The listing
			// is never sent, since SHOW COLUMNS on a missing table fails and
			// pins the session (be-bv7x).
			name: "behind without an issues table is refused",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = DATABASE()")).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				mock.ExpectQuery("^SHOW TABLES$").
					WillReturnRows(sqlmock.NewRows([]string{"Tables_in_testdb"}).AddRow("schema_migrations"))
			},
			check: requireBehindError(behind),
		},
		{
			// The #6929 read-path gap: issues complete, leases overlay
			// missing a column. The full projection's requirements refuse at
			// open instead of failing every read at query time.
			name:           "behind with a complete issues table but a short leases overlay is refused",
			fullProjection: true,
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectIssuesColumns(mock, "id", "arrival_seq")
				expectLeasesColumns(mock, "issue_id", "lease_expires_at", "heartbeat_at")
			},
			check: requireBehindError(behind),
		},
		{
			// A dropped leases table — the out-of-band materialization shape
			// the migration replay heals — reads as every column missing.
			name:           "behind without a leases table is refused, not a raw table-not-found",
			fullProjection: true,
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectIssuesColumns(mock, "id", "arrival_seq")
				expectCursorProbe(mock, "leases", false)
			},
			check: requireBehindError(behind),
		},
		{
			// The first requirement's gap is terminal: the second table is
			// never probed. sqlmock's strict ordering proves it — no leases
			// expectations exist to consume.
			name:           "a first-requirement gap stops the probes",
			fullProjection: true,
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectIssuesColumns(mock, "id")
			},
			check: requireBehindError(behind),
		},
		{
			// Both requirements satisfied: the behind database keeps serving.
			name:           "behind with both requirements complete stays readable",
			fullProjection: true,
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectIssuesColumns(mock, "id", "arrival_seq")
				expectLeasesColumns(mock, "issue_id", "lease_expires_at", "heartbeat_at", "granted_node")
			},
			check: requireNoDriftError,
		},
		{
			name: "a probe failure is wrapped, not mistaken for drift",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectCursorProbe(mock, "issues", true)
				mock.ExpectQuery(`^SHOW COLUMNS FROM issues$`).WillReturnError(fmt.Errorf("boom"))
			},
			check: func(t *testing.T, err error) {
				if err == nil || !strings.Contains(err.Error(), "schema drift check:") {
					t.Fatalf("CheckReadableDrift = %v, want a wrapped probe error", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			tc.expect(mock)
			reqs := issueOnlyRequirements(readableColumns)
			if tc.fullProjection {
				reqs = IssueReadableRequirements(readableColumns)
			}
			tc.check(t, CheckReadableDrift(context.Background(), db, reqs))
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

// TestIssueReadableRequirementsMatchSQLBuild pins the readable requirements
// against the SQL the shared projection actually issues — the join column and
// the columns LeaseSelectColumns selects — so a new lease column in a SELECT
// or JOIN fails here rather than as an unknown-column error at runtime. The
// sqlbuild import lives in this test only: the schema package itself stays
// unaware of query construction.
func TestIssueReadableRequirementsMatchSQLBuild(t *testing.T) {
	reqs := IssueReadableRequirements(sqlbuild.ColumnNames(sqlbuild.IssueBaseColumns))
	if len(reqs) != 2 || reqs[0].Table != "issues" || reqs[1].Table != "leases" {
		t.Fatalf("requirements = %+v, want exactly issues then leases", reqs)
	}
	if !reflect.DeepEqual(reqs[0].Columns, sqlbuild.ColumnNames(sqlbuild.IssueBaseColumns)) {
		t.Errorf("issues requirement = %v, want the shared projection's columns %v",
			reqs[0].Columns, sqlbuild.ColumnNames(sqlbuild.IssueBaseColumns))
	}
	join := sqlbuild.LeaseJoin("i")
	if !strings.Contains(join, "leases.issue_id") {
		t.Errorf("LeaseJoin = %q, expected it to reference leases.issue_id", join)
	}
	want := []string{"issue_id"}
	for _, col := range strings.Split(sqlbuild.LeaseSelectColumns, ", ") {
		want = append(want, strings.TrimPrefix(col, "leases."))
	}
	if !reflect.DeepEqual(reqs[1].Columns, want) {
		t.Errorf("leases requirement = %v, want the join column plus LeaseSelectColumns %v", reqs[1].Columns, want)
	}
}

func TestCheckReadableDrift_EscapeHatchReadsAnyway(t *testing.T) {
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	behind := LatestVersion() - 1
	expectMainCursor(mock, behind)
	expectIssuesColumns(mock, "id")

	origStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stderr = w
	gotErr := CheckReadableDrift(context.Background(), db, issueOnlyRequirements(readableColumns))
	_ = w.Close()
	os.Stderr = origStderr
	var buf bytes.Buffer
	if _, copyErr := io.Copy(&buf, r); copyErr != nil {
		t.Fatal(copyErr)
	}
	_ = r.Close()

	if gotErr != nil {
		t.Fatalf("CheckReadableDrift = %v, want nil when BD_IGNORE_SCHEMA_SKEW=1", gotErr)
	}
	wantWarning := fmt.Sprintf("database (v%d) is behind binary (v%d)", behind, LatestVersion())
	if !strings.Contains(buf.String(), wantWarning) {
		t.Errorf("stderr = %q, want a warning containing %q", buf.String(), wantWarning)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestSchemaBehindError_GatewayRemedy pins that a gateway's refusal does not
// send its reader to `bd migrate schema`, which bd never runs against a
// schema the gateway server owns.
func TestSchemaBehindError_GatewayRemedy(t *testing.T) {
	err := &SchemaBehindError{DBVersion: 72, BinaryVersion: 73, Gateway: true}
	msg := err.Error()
	for _, want := range []string{"database is at v72, binary expects v73", "gateway server owns", "server administrator", "schema v72"} {
		if !strings.Contains(msg, want) {
			t.Errorf("gateway refusal %q is missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "bd migrate schema") {
		t.Errorf("gateway refusal prescribes `bd migrate schema`: %q", msg)
	}
	if !IsSchemaBehindError(err) {
		t.Error("IsSchemaBehindError(gateway refusal) = false, want true")
	}
}

func requireNoDriftError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("CheckReadableDrift = %v, want nil", err)
	}
}

func requireBehindError(dbVersion int) func(t *testing.T, err error) {
	return func(t *testing.T, err error) {
		t.Helper()
		var behindErr *SchemaBehindError
		if !errors.As(err, &behindErr) {
			t.Fatalf("CheckReadableDrift = %T (%v), want *SchemaBehindError", err, err)
		}
		if behindErr.DBVersion != dbVersion || behindErr.BinaryVersion != LatestVersion() {
			t.Errorf("SchemaBehindError = v%d/v%d, want v%d/v%d", behindErr.DBVersion, behindErr.BinaryVersion, dbVersion, LatestVersion())
		}
		if behindErr.Gateway {
			t.Error("CheckReadableDrift set Gateway; only the gateway open path may")
		}
	}
}
