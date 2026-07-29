package doctor

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

var errTableNotFound = errors.New("table not found: wisp_dependencies")

// The sqlmock tests pin the loader's failure-path and fallback-selection
// contracts without a live server; integrity_cycles_db_test.go covers the
// same loader against real Dolt. Ordered expectations also pin that every
// query runs inside the single BeginTx snapshot.

func expectIDPaginationProbe(mock sqlmock.Sqlmock, table string, usable bool) {
	cols := mock.ExpectQuery(`information_schema\.columns`).WithArgs(table)
	if !usable {
		cols.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		return
	}
	cols.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	// No rows → sql.ErrNoRows → no NULL ids → pagination usable.
	mock.ExpectQuery(`SELECT 1 FROM ` + table + ` WHERE id IS NULL`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
}

// TestLoadDependencyEdges_ScanErrorFailsCheck: a row that fails to scan must
// fail the check. Skipping it would leave rowsRead short of the page size,
// silently ending pagination with a truncated graph and a false "no cycles".
func TestLoadDependencyEdges_ScanErrorFailsCheck(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	expectIDPaginationProbe(mock, "dependencies", true)
	// NULL id cannot scan into string → Scan error mid-page.
	mock.ExpectQuery(`FROM dependencies\s+WHERE id > \?`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "depends_on_id", "type"}).
			AddRow("cycdep-1", "a", "b", "blocks").
			AddRow(nil, "b", "a", "blocks"))
	mock.ExpectRollback()

	edges, check := loadDependencyEdges(db, 10, 100)
	if edges != nil || check == nil {
		t.Fatalf("edges = %v, check = %v; want nil edges and a failing check", edges, check)
	}
	if check.Status != StatusWarning || !strings.Contains(check.Detail, "scan dependencies") {
		t.Fatalf("check = %q (%s: %s), want warning wrapping the scan error", check.Status, check.Message, check.Detail)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestLoadDependencyEdges_MissingIDColumnFallsBack: when the id column is
// absent (the un-repaired #4690 shape doctor's read-only open can encounter),
// the loader must fall back to a full scan of that table rather than failing
// or paginating unsoundly — while tables with a healthy id still paginate.
func TestLoadDependencyEdges_MissingIDColumnFallsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	expectIDPaginationProbe(mock, "dependencies", false)
	// Fallback: one unpaginated scan (no `WHERE id > ?`).
	mock.ExpectQuery(`AS depends_on_id, type\s+FROM dependencies$`).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "depends_on_id", "type"}).
			AddRow("a", "b", "blocks").
			AddRow("b", "a", "conditional-blocks").
			AddRow("a", "c", "related"))
	expectIDPaginationProbe(mock, "wisp_dependencies", true)
	mock.ExpectQuery(`FROM wisp_dependencies\s+WHERE id > \?`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "depends_on_id", "type"}).
			AddRow("w-dep-1", "w1", "a", "blocks"))
	mock.ExpectRollback()

	edges, check := loadDependencyEdges(db, 10, 100)
	if check != nil {
		t.Fatalf("unexpected check %s: %s", check.Message, check.Detail)
	}
	want := map[string][]string{"a": {"b"}, "b": {"a"}, "w1": {"a"}}
	if !reflect.DeepEqual(edges, want) {
		t.Fatalf("edges = %v, want %v", edges, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestLoadDependencyEdges_QueryErrorFailsCheck: a failed page query (e.g. a
// missing wisp_dependencies table on a clone that has not run the repair
// migrations) must surface as the warning check, exactly like 'bd dep cycles'
// erroring on the same shape — never as a silent "no cycles".
func TestLoadDependencyEdges_QueryErrorFailsCheck(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	expectIDPaginationProbe(mock, "dependencies", true)
	mock.ExpectQuery(`FROM dependencies\s+WHERE id > \?`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "depends_on_id", "type"}))
	expectIDPaginationProbe(mock, "wisp_dependencies", false)
	mock.ExpectQuery(`AS depends_on_id, type\s+FROM wisp_dependencies$`).
		WillReturnError(errTableNotFound)
	mock.ExpectRollback()

	edges, check := loadDependencyEdges(db, 10, 100)
	if edges != nil || check == nil {
		t.Fatalf("edges = %v, check = %v; want nil edges and a failing check", edges, check)
	}
	if check.Status != StatusWarning || !strings.Contains(check.Detail, "wisp_dependencies") {
		t.Fatalf("check = %q (%s: %s), want warning naming the failing table", check.Status, check.Message, check.Detail)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
