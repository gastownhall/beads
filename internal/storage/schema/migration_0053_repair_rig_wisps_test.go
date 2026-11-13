package schema

import (
	"context"
	"io/fs"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestEnsureWispsColumnsForMigration0053CoversColumnsAddedAfter0020 keeps the
// #6080 repair honest against the frozen SQL it exists to protect. Migration
// 0053 copies a fixed column list from the clone-local wisps table into
// issues; every one of those columns that migration 0020 did NOT create can
// be missing on a clone whose wisps table was built later from an older
// shape, because the only migrations that add such columns (0023, 0027) no-op
// unless wisps already exists when they run — and wisps never syncs between
// clones. That is exactly the gap ensureWispsLateColumns repairs, so the
// repair's column set must equal 0053's selected columns minus 0020's created
// columns, no more and no less, and each ADD COLUMN it issues must be
// byte-identical to the original migration's statement.
//
// The parsing is deliberately narrow (substring extraction on known frozen
// files, not a SQL parser): a change to any of these shipped files re-pins
// the freeze hashes, and any future migration that widens 0053's wisps column
// list or adds a new wisps column fails here until the repair is extended to
// cover it.
func TestEnsureWispsColumnsForMigration0053CoversColumnsAddedAfter0020(t *testing.T) {
	const (
		file0020 = "migrations/0020_create_wisps.up.sql"
		file0023 = "migrations/0023_add_no_history_column.up.sql"
		file0027 = "migrations/0027_add_started_at.up.sql"
		file0053 = "migrations/0053_repair_rig_wisps.up.sql"
	)
	readMigration := func(name string) string {
		t.Helper()
		body, err := fs.ReadFile(upMigrations, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(body)
	}

	// 0053's frozen INSERT ... SELECT: the issues column list must match the
	// wisps SELECT list (the copy is symmetric), and both name every wisps
	// column the migration requires to exist.
	body0053 := readMigration(file0053)
	insertAt := strings.Index(body0053, "INSERT IGNORE INTO issues (")
	selectAt := strings.Index(body0053, ") SELECT ")
	fromAt := strings.Index(body0053, " FROM wisps ")
	if insertAt < 0 || selectAt < insertAt || fromAt < selectAt {
		t.Fatalf("%s: could not locate the rig INSERT ... SELECT block", file0053)
	}
	splitCols := func(list string) []string {
		var cols []string
		for _, col := range strings.Split(list, ",") {
			if col = strings.TrimSpace(col); col != "" {
				cols = append(cols, col)
			}
		}
		return cols
	}
	issueCols := splitCols(body0053[insertAt+len("INSERT IGNORE INTO issues (") : selectAt])
	wispCols := splitCols(body0053[selectAt+len(") SELECT ") : fromAt])
	if len(issueCols) == 0 || len(wispCols) == 0 {
		t.Fatalf("%s: extracted an empty column list (issues %d, wisps %d)", file0053, len(issueCols), len(wispCols))
	}
	if !reflect.DeepEqual(issueCols, wispCols) {
		t.Fatalf("%s: issues INSERT columns and wisps SELECT columns disagree:\n issues: %v\n wisps:  %v", file0053, issueCols, wispCols)
	}

	// 0020's created columns: the table body's first token per line, up to
	// the first index line. Nothing else in the CREATE block is a column.
	created0020 := map[string]bool{}
	for _, line := range strings.Split(readMigration(file0020), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "CREATE TABLE"), trimmed == "(", trimmed == ");", trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "INDEX"), strings.HasPrefix(trimmed, "UNIQUE"), strings.HasPrefix(trimmed, "CONSTRAINT"), strings.HasPrefix(trimmed, "PRIMARY KEY"):
			// Column list is over; keep scanning harmlessly.
		default:
			created0020[strings.Fields(trimmed)[0]] = true
		}
	}
	if len(created0020) == 0 {
		t.Fatalf("%s: parsed no created columns", file0020)
	}

	// The late columns: what 0053 reads that 0020 did not create.
	var late []string
	for _, col := range wispCols {
		if !created0020[col] {
			late = append(late, col)
		}
	}
	sort.Strings(late)

	// 0023/0027's wisps ADD COLUMN statements, quoted verbatim out of the
	// frozen bodies. Their column set must be exactly the late columns, and
	// the repair must issue their DDL byte-for-byte.
	alterRe := regexp.MustCompile(`ALTER TABLE wisps ADD COLUMN (\w+) ([^']+)`)
	type addColumn struct{ name, ddl string }
	var adds []addColumn
	for _, name := range []string{file0023, file0027} {
		matches := alterRe.FindAllStringSubmatch(readMigration(name), -1)
		if len(matches) != 1 {
			t.Fatalf("%s: found %d wisps ADD COLUMN statements, want exactly 1", name, len(matches))
		}
		adds = append(adds, addColumn{matches[0][1], matches[0][2]})
	}
	var added []string
	for _, add := range adds {
		added = append(added, add.name)
	}
	sort.Strings(added)
	if !reflect.DeepEqual(late, added) {
		t.Fatalf("columns 0053 reads but 0020 did not create = %v, while 0023/0027 add %v; ensureWispsLateColumns covers one side only — extend the repair or the guard to match",
			late, added)
	}

	// The repair, driven through a mock wisps table missing both columns,
	// must issue each original ADD COLUMN verbatim, then re-probe as a no-op
	// on a repeated invocation.
	failOnSwallowedAdvisory(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	tableQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES.*TABLE_NAME = \?`
	columnQuery := `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS.*TABLE_NAME = \? AND COLUMN_NAME = \?`
	count := func(n int) *sqlmock.Rows { return sqlmock.NewRows([]string{"c"}).AddRow(n) }

	mock.ExpectQuery(tableQuery).WithArgs("wisps").WillReturnRows(count(1))
	for _, add := range adds {
		mock.ExpectQuery(columnQuery).WithArgs("wisps", add.name).WillReturnRows(count(0))
		mock.ExpectExec("^" + regexp.QuoteMeta("ALTER TABLE wisps ADD COLUMN "+add.name+" "+add.ddl) + "$").
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	if err := ensureWispsLateColumns(context.Background(), db); err != nil {
		t.Fatalf("ensureWispsLateColumns with both late columns missing: %v", err)
	}

	mock.ExpectQuery(tableQuery).WithArgs("wisps").WillReturnRows(count(1))
	for _, add := range adds {
		mock.ExpectQuery(columnQuery).WithArgs("wisps", add.name).WillReturnRows(count(1))
	}
	if err := ensureWispsLateColumns(context.Background(), db); err != nil {
		t.Fatalf("ensureWispsLateColumns repeated invocation: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations (a probe or ALTER the repair did not owe, or in the wrong order): %v", err)
	}
}
