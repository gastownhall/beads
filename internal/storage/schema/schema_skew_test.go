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

// readableRelations stands in for the relation set a caller passes: a
// required row table, an optional one a database may lack, and a required
// overlay whose columns include a join key.
var readableRelations = []sqlbuild.ReadRelation{
	{Table: "issues", Columns: []string{"id", "arrival_seq"}},
	{Table: "wisps", Columns: []string{"id", "arrival_seq"}, Optional: true},
	{Table: "leases", Columns: []string{"issue_id", "granted_node"}},
}

func expectMainCursor(mock sqlmock.Sqlmock, version int) {
	expectCursorProbe(mock, "schema_migrations", true)
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(version))
}

// expectRelationColumns mocks relationColumns' read of one table: the table
// probe, then — when the table exists — the SHOW COLUMNS listing with these
// names in its Field column. The SHOW TABLES answer always lists some other
// table so the probed table's presence or absence is the only variable.
func expectRelationColumns(mock sqlmock.Sqlmock, table string, exists bool, columns ...string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = DATABASE()")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	baseline := "issues"
	if table == "issues" {
		baseline = "schema_migrations"
	}
	rows := sqlmock.NewRows([]string{"Tables_in_testdb"}).AddRow(baseline)
	if exists {
		rows.AddRow(table)
	}
	mock.ExpectQuery("^SHOW TABLES$").WillReturnRows(rows)
	if !exists {
		return
	}
	cols := sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"})
	for _, column := range columns {
		cols.AddRow(column, "bigint", "YES", "", nil, "")
	}
	mock.ExpectQuery("^SHOW COLUMNS FROM " + table + "$").WillReturnRows(cols)
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
			// version read: no relation probe is expected.
			name:   "at latest",
			expect: func(mock sqlmock.Sqlmock) { expectMainCursor(mock, LatestVersion()) },
			check:  requireNoDriftError,
		},
		{
			// Same single read for a database ahead of the binary: the
			// forward-skew refusal needs no relation evidence.
			name:   "ahead is refused as forward drift",
			expect: func(mock sqlmock.Sqlmock) { expectMainCursor(mock, LatestVersion()+1) },
			check: func(t *testing.T, err error) {
				if !IsSchemaSkewError(err) {
					t.Fatalf("CheckReadableDrift = %T (%v), want *SchemaSkewError", err, err)
				}
			},
		},
		{
			// bd-578h9.5 / #5920: a behind database whose relations are all
			// readable keeps serving reads. Column names compare
			// case-insensitively.
			name: "behind with every relation readable stays open",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectRelationColumns(mock, "issues", true, "id", "ARRIVAL_SEQ")
				expectRelationColumns(mock, "wisps", true, "ID", "arrival_seq")
				expectRelationColumns(mock, "leases", true, "issue_id", "GRANTED_NODE")
			},
			check: requireNoDriftError,
		},
		{
			name: "behind and missing a row column is refused",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectRelationColumns(mock, "issues", true, "id")
				expectRelationColumns(mock, "wisps", true, "id", "arrival_seq")
				expectRelationColumns(mock, "leases", true, "issue_id", "granted_node")
			},
			check: requireBehindError(behind, "issues.arrival_seq"),
		},
		{
			// No issues table at all: the whole required relation is
			// missing. The listing is never sent, since SHOW COLUMNS on a
			// missing table fails and pins the session (be-bv7x).
			name: "behind without the required row table is refused",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectRelationColumns(mock, "issues", false)
				expectRelationColumns(mock, "wisps", true, "id", "arrival_seq")
				expectRelationColumns(mock, "leases", true, "issue_id", "granted_node")
			},
			check: requireBehindError(behind, "issues"),
		},
		{
			// #6572: the leases overlay is clone-local (dolt_ignored), so a
			// behind clone can lack it entirely while the versioned rows are
			// intact. The refusal names the bare relation.
			name: "behind without the leases overlay is refused",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectRelationColumns(mock, "issues", true, "id", "arrival_seq")
				expectRelationColumns(mock, "wisps", true, "id", "arrival_seq")
				expectRelationColumns(mock, "leases", false)
			},
			check: requireBehindError(behind, "leases"),
		},
		{
			name: "behind with a lease column missing is refused",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectRelationColumns(mock, "issues", true, "id", "arrival_seq")
				expectRelationColumns(mock, "wisps", true, "id", "arrival_seq")
				expectRelationColumns(mock, "leases", true, "issue_id")
			},
			check: requireBehindError(behind, "leases.granted_node"),
		},
		{
			// An optional relation a database may lack (the wisp plane)
			// reads as absent-and-fine, not as unreadable schema.
			name: "behind without the optional wisp table stays open",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectRelationColumns(mock, "issues", true, "id", "arrival_seq")
				expectRelationColumns(mock, "wisps", false)
				expectRelationColumns(mock, "leases", true, "issue_id", "granted_node")
			},
			check: requireNoDriftError,
		},
		{
			// A wisp table that exists but lacks a projected column still
			// breaks the get-by-id fallback, which selects the same
			// projection from it.
			name: "behind wisp table missing a column is refused",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectRelationColumns(mock, "issues", true, "id", "arrival_seq")
				expectRelationColumns(mock, "wisps", true, "id")
				expectRelationColumns(mock, "leases", true, "issue_id", "granted_node")
			},
			check: requireBehindError(behind, "wisps.arrival_seq"),
		},
		{
			// The evidence accumulates across relations in the descriptor's
			// order, deterministically, so the refusal names everything at
			// once.
			name: "missing identifiers aggregate in relation order",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectRelationColumns(mock, "issues", true, "id")
				expectRelationColumns(mock, "wisps", true, "id")
				expectRelationColumns(mock, "leases", false)
			},
			check: requireBehindError(behind, "issues.arrival_seq", "wisps.arrival_seq", "leases"),
		},
		{
			// A probe that fails mid-way is the caller's error to report,
			// not a false verdict about the schema.
			name: "probe failure propagates",
			expect: func(mock sqlmock.Sqlmock) {
				expectMainCursor(mock, behind)
				expectRelationColumns(mock, "issues", true, "id", "arrival_seq")
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = DATABASE()")).
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				mock.ExpectQuery("^SHOW TABLES$").
					WillReturnError(errors.New("catalog gone"))
			},
			check: func(t *testing.T, err error) {
				if err == nil || !strings.Contains(err.Error(), "catalog gone") {
					t.Fatalf("CheckReadableDrift = %v, want the wrapped probe failure", err)
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
			tc.check(t, CheckReadableDrift(context.Background(), db, readableRelations))
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
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
	expectRelationColumns(mock, "issues", true, "id")
	expectRelationColumns(mock, "wisps", true, "id", "arrival_seq")
	expectRelationColumns(mock, "leases", false)

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
	if !strings.Contains(buf.String(), "some queries may fail") {
		t.Errorf("stderr = %q, want the escape hatch to say queries may fail", buf.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestSchemaBehindError_Missing pins the evidence-bearing arm of the behind
// refusal: it names what the reads cannot find, in both the plain and gateway
// remedies, while a version-only refusal (no evidence) keeps its message.
func TestSchemaBehindError_Missing(t *testing.T) {
	withEvidence := &SchemaBehindError{DBVersion: 72, BinaryVersion: 73, Missing: []string{"leases", "leases.granted_node"}}
	msg := withEvidence.Error()
	for _, want := range []string{"v72", "v73", "leases", "leases.granted_node"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q is missing %q", msg, want)
		}
	}
	gateway := &SchemaBehindError{DBVersion: 72, BinaryVersion: 73, Gateway: true, Missing: []string{"leases.granted_node"}}
	if got := gateway.Error(); !strings.Contains(got, "leases.granted_node") || !strings.Contains(got, "server administrator") {
		t.Errorf("gateway refusal %q lost its evidence or its remedy", got)
	}
	// No evidence, no naming: the strict version-only checks keep the
	// message operators match on.
	plain := &SchemaBehindError{DBVersion: 72, BinaryVersion: 73}
	if got := plain.Error(); strings.Contains(got, "lacks") {
		t.Errorf("version-only refusal %q claims missing identifiers it never collected", got)
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

func requireBehindError(dbVersion int, missing ...string) func(t *testing.T, err error) {
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
		if got := strings.Join(behindErr.Missing, ", "); got != strings.Join(missing, ", ") {
			t.Errorf("SchemaBehindError.Missing = %q, want %q", got, strings.Join(missing, ", "))
		}
	}
}
