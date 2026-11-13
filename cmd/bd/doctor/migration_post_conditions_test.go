package doctor

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// GH#5269, AC-7: the doctor surface of the read-only dropped-default
// diagnosis. Healthy applicable conditions are OK; every applicable
// violation warns with the `bd migrate schema` remedy; a store that cannot be
// inspected warns could-not-check instead of reading as health.
//
// postConditionProbeOrder is the covered obligations' probe order (sorted by
// table, then column).
var postConditionProbeOrder = []string{
	"comments", "compaction_snapshots", "dependencies", "events",
	"issue_snapshots", "wisp_comments", "wisp_dependencies", "wisp_events",
}

func expectPostConditionCursors(mock sqlmock.Sqlmock, mainVersion, ignoredVersion int) {
	mock.ExpectQuery(`SELECT MAX\(version\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(mainVersion))
	mock.ExpectQuery(`SELECT MAX\(version\) FROM ignored_schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(ignoredVersion))
}

func expectPostConditionAbsentCursors(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT MAX\(version\) FROM schema_migrations`).
		WillReturnError(errors.New("Error 1146 (42S02): Table `schema_migrations` doesn't exist"))
	mock.ExpectQuery(`SELECT MAX\(version\) FROM ignored_schema_migrations`).
		WillReturnError(errors.New("Error 1146 (42S02): Table `ignored_schema_migrations` doesn't exist"))
}

// expectPostConditionProbes stubs the table-exists and column-default probes
// for every covered obligation, in probe order; drifted lists the tables
// whose id column still carries the default a recorded migration promised to
// drop.
func expectPostConditionProbes(mock sqlmock.Sqlmock, drifted ...string) {
	driftedSet := make(map[string]bool, len(drifted))
	for _, table := range drifted {
		driftedSet[table] = true
	}
	for _, table := range postConditionProbeOrder {
		mock.ExpectQuery(`^SHOW TABLES$`).
			WillReturnRows(sqlmock.NewRows([]string{"Tables_in_beads"}).AddRow(table))
		def := driver.Value(nil)
		if driftedSet[table] {
			def = "(uuid())"
		}
		mock.ExpectQuery(`^SHOW COLUMNS FROM ` + table + ` LIKE 'id'$`).
			WillReturnRows(sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
				AddRow("id", "char(36)", "NO", "PRI", def, ""))
	}
}

func TestCheckMigrationPostConditions_NoDatabase(t *testing.T) {
	got := CheckMigrationPostConditions(&SharedStore{}) // Store() == nil, no beads dir
	if got.Status != StatusOK {
		t.Errorf("status = %q, want %q", got.Status, StatusOK)
	}
	if got.Message != "N/A (no database)" {
		t.Errorf("message = %q, want %q", got.Message, "N/A (no database)")
	}
}

func TestCheckMigrationPostConditions_Healthy(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectPostConditionCursors(mock, 74, 31)
	expectPostConditionProbes(mock)

	got := checkMigrationPostConditions(context.Background(), db)
	if got.Status != StatusOK {
		t.Fatalf("status = %q, want %q (%s)", got.Status, StatusOK, got.Message)
	}
	if !strings.Contains(got.Message, "hold") {
		t.Errorf("message = %q, want it to say the post-conditions hold", got.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCheckMigrationPostConditions_NoRecordedMigrations(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectPostConditionAbsentCursors(mock)

	got := checkMigrationPostConditions(context.Background(), db)
	if got.Status != StatusOK {
		t.Fatalf("status = %q, want %q (%s)", got.Status, StatusOK, got.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// The reporter's state: marks current, defaults back. Every applicable
// violation is named, with the `bd migrate schema` remedy.
func TestCheckMigrationPostConditions_DriftWarns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expectPostConditionCursors(mock, 74, 31)
	expectPostConditionProbes(mock, "comments", "dependencies")

	got := checkMigrationPostConditions(context.Background(), db)
	if got.Status != StatusWarning {
		t.Fatalf("status = %q, want %q (%s)", got.Status, StatusWarning, got.Message)
	}
	if !strings.Contains(got.Message, "2") {
		t.Errorf("message = %q, want it to count the violations", got.Message)
	}
	for _, want := range []string{"comments.id (v0051", "dependencies.id (v0043"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail = %q, want it to name %q", got.Detail, want)
		}
	}
	if !strings.Contains(got.Fix, "bd migrate schema") {
		t.Errorf("fix = %q, want it to name the `bd migrate schema` remedy", got.Fix)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// An unexpected read failure warns could-not-check; it must not read as OK
// (the bd-6dnrw.27 lesson: a broken query behind a check is a permanent
// silent no-op).
func TestCheckMigrationPostConditions_UnexpectedErrorWarns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT MAX\(version\) FROM schema_migrations`).
		WillReturnError(errors.New("dolt: read boom"))

	got := checkMigrationPostConditions(context.Background(), db)
	if got.Status != StatusWarning {
		t.Fatalf("status = %q, want %q (%s)", got.Status, StatusWarning, got.Message)
	}
	if !strings.Contains(got.Message, "Could not check migration post-conditions") {
		t.Errorf("message = %q, want a 'Could not check' diagnostic", got.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
