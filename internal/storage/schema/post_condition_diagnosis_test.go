package schema

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// GH#5269, AC-7: the read-only diagnosis behind `bd doctor`'s Migration
// Post-Conditions check. It re-derives the dropped-default obligations the
// RECORDED marks claim and reports the ones whose default survived, touching
// nothing — sqlmock fails any statement it did not expect, and no correction
// ALTER or cursor write is expected anywhere in this file. The doctor and
// embedded tiers pin the same behaviour end to end.
//
// diagnosisProbeOrder is the covered obligations' probe order:
// coveredDroppedDefaultObligations sorts by table, then column.
var diagnosisProbeOrder = []string{
	"comments", "compaction_snapshots", "dependencies", "events",
	"issue_snapshots", "wisp_comments", "wisp_dependencies", "wisp_events",
}

// expectDiagnosisCursors stubs the two recorded-cursor reads.
func expectDiagnosisCursors(mock sqlmock.Sqlmock, mainVersion, ignoredVersion int) {
	mock.ExpectQuery(`SELECT MAX\(version\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(mainVersion))
	mock.ExpectQuery(`SELECT MAX\(version\) FROM ignored_schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(ignoredVersion))
}

// expectDiagnosisAbsentCursor stubs a cursor read as table-not-exist: a
// cursor table that does not exist records nothing.
func expectDiagnosisAbsentCursor(mock sqlmock.Sqlmock, cursorTable string) {
	mock.ExpectQuery(`SELECT MAX\(version\) FROM ` + cursorTable).
		WillReturnError(errors.New("Error 1146 (42S02): Table `" + cursorTable + "` doesn't exist"))
}

// expectDiagnosisProbes stubs the table-exists and column-default probes for
// the covered obligations, in probe order; drifted lists the tables whose id
// column still carries the default a recorded migration promised to drop.
func expectDiagnosisProbes(mock sqlmock.Sqlmock, tables []string, drifted ...string) {
	driftedSet := make(map[string]bool, len(drifted))
	for _, table := range drifted {
		driftedSet[table] = true
	}
	for _, table := range tables {
		mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables(table))
		def := driver.Value(nil)
		if driftedSet[table] {
			def = "(uuid())"
		}
		mock.ExpectQuery(`^SHOW COLUMNS FROM ` + table + ` LIKE 'id'$`).
			WillReturnRows(idDefault(def))
	}
}

// A store whose marks never got ahead of its DDL reports no violations, and
// the diagnosis issues no DDL of its own.
func TestDiagnoseDroppedDefaultDriftHealthyStoreReportsNoViolations(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectDiagnosisCursors(mock, 74, 31)
	expectDiagnosisProbes(mock, diagnosisProbeOrder)

	violations, err := DiagnoseDroppedDefaultDrift(context.Background(), db)
	if err != nil {
		t.Fatalf("DiagnoseDroppedDefaultDrift: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none", violations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// Every applicable violation is reported, each naming its column, its
// provenance (the lowest recorded version that promised the drop, per the
// table/column dedup) and the default that survived.
func TestDiagnoseDroppedDefaultDriftReportsEveryRecordedViolation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectDiagnosisCursors(mock, 74, 31)
	expectDiagnosisProbes(mock, diagnosisProbeOrder, "comments", "dependencies")

	violations, err := DiagnoseDroppedDefaultDrift(context.Background(), db)
	if err != nil {
		t.Fatalf("DiagnoseDroppedDefaultDrift: %v", err)
	}
	want := []string{
		"comments.id (v0051, schema_migrations): DEFAULT (uuid()) still present",
		"dependencies.id (v0043, schema_migrations): DEFAULT (uuid()) still present",
	}
	if strings.Join(violations, "|") != strings.Join(want, "|") {
		t.Fatalf("violations =\n%v\nwant\n%v", violations, want)
	}
	// No correction ALTER, no cursor write: the diagnosis is read-only, and
	// sqlmock would have failed an unexpected statement at call time.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// Only the obligations the recorded marks cover are probed: a store current
// on the main series whose ignored cursor table does not exist owes nothing
// on the wisp tables, and their columns are never read.
func TestDiagnoseDroppedDefaultDriftProbesOnlyCoveredObligations(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT MAX\(version\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(51))
	expectDiagnosisAbsentCursor(mock, "ignored_schema_migrations")
	// Main 0043/0050/0051 obligations only; the wisp_* expectations are
	// deliberately absent — ExpectationsWereMet proves they never ran.
	expectDiagnosisProbes(mock, []string{
		"comments", "compaction_snapshots", "dependencies", "events", "issue_snapshots",
	})

	violations, err := DiagnoseDroppedDefaultDrift(context.Background(), db)
	if err != nil {
		t.Fatalf("DiagnoseDroppedDefaultDrift: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none", violations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// A fresh store with no recorded marks owes no post-conditions; the absent
// cursor tables read as recording nothing, and no probe runs.
func TestDiagnoseDroppedDefaultDriftNoRecordedMarksOwesNothing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectDiagnosisAbsentCursor(mock, "schema_migrations")
	expectDiagnosisAbsentCursor(mock, "ignored_schema_migrations")

	violations, err := DiagnoseDroppedDefaultDrift(context.Background(), db)
	if err != nil {
		t.Fatalf("DiagnoseDroppedDefaultDrift: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none", violations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// A cursor that cannot be read fails the diagnosis loudly: a broken query
// behind a check must not read as health (the bd-6dnrw.27 lesson).
func TestDiagnoseDroppedDefaultDriftUnreadableCursorFailsLoud(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT MAX\(version\) FROM schema_migrations`).
		WillReturnError(errors.New("dolt: read boom"))

	violations, err := DiagnoseDroppedDefaultDrift(context.Background(), db)
	if err == nil {
		t.Fatalf("unreadable cursor read as health (violations %v)", violations)
	}
	if !strings.Contains(err.Error(), "schema_migrations") {
		t.Errorf("error = %q, want it to name the cursor table", err)
	}
	if len(violations) != 0 {
		t.Errorf("violations = %v, want none alongside the error", violations)
	}
}

// A column that cannot be probed fails the diagnosis the same way: a partial
// drift picture is not a clean bill of health.
func TestDiagnoseDroppedDefaultDriftProbeFailureFailsLoud(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectDiagnosisCursors(mock, 74, 31)
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("comments"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM comments LIKE 'id'$`).
		WillReturnError(errors.New("dolt: read boom"))

	violations, err := DiagnoseDroppedDefaultDrift(context.Background(), db)
	if err == nil {
		t.Fatalf("probe failure read as health (violations %v)", violations)
	}
	if len(violations) != 0 {
		t.Errorf("violations = %v, want none alongside the error", violations)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
