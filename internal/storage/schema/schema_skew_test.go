package schema

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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

// readableRelations is the issues-only relation set the original
// TestCheckReadableDrift cases were written against.
var readableRelations = []sqlbuild.ReadRelation{{Table: "issues", Columns: readableColumns}}

func expectMainCursor(mock sqlmock.Sqlmock, version int) {
	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(version))
}

// expectIssuesColumns mocks missingColumns' read of issues: the table probe,
// then the SHOW COLUMNS listing with these names in its Field column.
func expectIssuesColumns(mock sqlmock.Sqlmock, columns ...string) {
	expectTableColumns(mock, "issues", columns...)
}

// expectTableColumns is expectIssuesColumns for any table.
func expectTableColumns(mock sqlmock.Sqlmock, table string, columns ...string) {
	expectCursorProbe(mock, table, true)
	rows := sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"})
	for _, column := range columns {
		rows.AddRow(column, "bigint", "YES", "", nil, "")
	}
	mock.ExpectQuery(`^SHOW COLUMNS FROM ` + table + `$`).WillReturnRows(rows)
}

func TestCheckReadableDrift(t *testing.T) {
	behind := LatestVersion() - 1
	for _, tc := range []struct {
		name   string
		expect func(sqlmock.Sqlmock)
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			tc.expect(mock)
			tc.check(t, CheckReadableDrift(context.Background(), db, readableRelations))
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

// overlayRelations is a small stand-in for sqlbuild.IssueReadRelations: a
// required issues row, an optional wisps row and a required leases overlay.
var overlayRelations = []sqlbuild.ReadRelation{
	{Table: "issues", Columns: []string{"id", "storage_class"}},
	{Table: "wisps", Columns: []string{"id", "storage_class"}, Optional: true},
	{Table: "leases", Columns: []string{"issue_id", "granted_node"}},
}

// TestCheckReadableDrift_Relations covers gastownhall/beads#6572: a behind
// database whose issues row is intact can still be unreadable, because issue
// hydration also joins the clone-local leases table and falls back to wisps.
func TestCheckReadableDrift_Relations(t *testing.T) {
	behind := LatestVersion() - 1
	for _, tc := range []struct {
		name   string
		expect func(sqlmock.Sqlmock)
		// missing is the SchemaBehindError.Missing wanted; nil wants no error.
		missing []string
	}{
		{
			// Still one version read: no relation is probed at latest.
			name:   "at latest probes no relation",
			expect: func(mock sqlmock.Sqlmock) { expectMainCursor(mock, LatestVersion()) },
		},
		{
			name: "behind with every relation intact stays readable",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectTableColumns(mock, "issues", "id", "storage_class")
				expectTableColumns(mock, "wisps", "ID", "Storage_Class")
				expectTableColumns(mock, "leases", "issue_id", "granted_node")
			},
		},
		{
			name: "an absent optional relation is not missing",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectTableColumns(mock, "issues", "id", "storage_class")
				expectCursorProbe(mock, "wisps", false)
				expectTableColumns(mock, "leases", "issue_id", "granted_node")
			},
		},
		{
			// The listing is never sent to the absent table (be-bv7x).
			name: "an absent required relation is refused by name",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectTableColumns(mock, "issues", "id", "storage_class")
				expectTableColumns(mock, "wisps", "id", "storage_class")
				expectCursorProbe(mock, "leases", false)
			},
			missing: []string{"leases"},
		},
		{
			name: "missing columns of present relations are all named, in order",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectTableColumns(mock, "issues", "id", "storage_class")
				expectTableColumns(mock, "wisps", "id")
				expectTableColumns(mock, "leases", "issue_id")
			},
			missing: []string{"wisps.storage_class", "leases.granted_node"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			tc.expect(mock)
			err = CheckReadableDrift(context.Background(), db, overlayRelations)
			if tc.missing == nil {
				requireNoDriftError(t, err)
			} else {
				requireBehindError(behind)(t, err)
				var behindErr *SchemaBehindError
				_ = errors.As(err, &behindErr)
				if fmt.Sprint(behindErr.Missing) != fmt.Sprint(tc.missing) {
					t.Errorf("Missing = %v, want %v", behindErr.Missing, tc.missing)
				}
				for _, name := range tc.missing {
					if !strings.Contains(err.Error(), name) {
						t.Errorf("error %q does not name %s", err, name)
					}
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}

	t.Run("a failed probe is an error, not a verdict", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		expectMainCursor(mock, behind)
		expectTableColumns(mock, "issues", "id", "storage_class")
		expectCursorProbe(mock, "wisps", false)
		expectCursorProbe(mock, "leases", true)
		probeErr := errors.New("connection reset")
		mock.ExpectQuery(`^SHOW COLUMNS FROM leases$`).WillReturnError(probeErr)
		err = CheckReadableDrift(context.Background(), db, overlayRelations)
		if !errors.Is(err, probeErr) || IsSchemaBehindError(err) {
			t.Fatalf("CheckReadableDrift = %v, want the probe error, not a *SchemaBehindError", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})
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
	gotErr := CheckReadableDrift(context.Background(), db, readableRelations)
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

// TestCheckReadableDrift_EscapeHatchWarnsWhatReadsLack pins the escape hatch
// on a schema whose issues projection is intact but which lacks the leases
// overlay every issue read joins (gastownhall/beads#6572): the open goes
// ahead, and the warning says what is missing and that queries may fail
// rather than reading on silently.
func TestCheckReadableDrift_EscapeHatchWarnsWhatReadsLack(t *testing.T) {
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	behind := LatestVersion() - 1
	expectMainCursor(mock, behind)
	expectTableColumns(mock, "issues", "id", "storage_class")
	expectTableColumns(mock, "wisps", "id", "storage_class")
	expectCursorProbe(mock, "leases", false)

	var gotErr error
	stderr := captureStderr(t, func() {
		gotErr = CheckReadableDrift(context.Background(), db, overlayRelations)
	})
	if gotErr != nil {
		t.Fatalf("CheckReadableDrift = %v, want nil when BD_IGNORE_SCHEMA_SKEW=1", gotErr)
	}
	for _, want := range []string{
		fmt.Sprintf("database (v%d) is behind binary (v%d)", behind, LatestVersion()),
		"lacks leases",
		"some queries may fail",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want a skew warning containing %q", stderr, want)
		}
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

// TestSchemaBehindError_Missing pins that a refusal found by the readability
// check names what is missing, and one on the version alone reads as before.
func TestSchemaBehindError_Missing(t *testing.T) {
	plain := (&SchemaBehindError{DBVersion: 72, BinaryVersion: 73}).Error()
	if strings.Contains(plain, "reads need") {
		t.Errorf("version-only refusal names missing relations: %q", plain)
	}
	for _, gateway := range []bool{false, true} {
		err := &SchemaBehindError{DBVersion: 72, BinaryVersion: 73, Gateway: gateway, Missing: []string{"leases", "wisps.storage_class"}}
		if msg := err.Error(); !strings.Contains(msg, "its reads need leases, wisps.storage_class, which this database lacks") {
			t.Errorf("Gateway=%v refusal %q does not name what is missing", gateway, msg)
		}
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
