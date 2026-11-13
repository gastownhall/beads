package schema

import (
	"context"
	"database/sql/driver"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

var showColumnsCols = []string{"Field", "Type", "Null", "Key", "Default", "Extra"}

// idDefault is a SHOW COLUMNS row for an id column; def nil means no default.
func idDefault(def driver.Value) *sqlmock.Rows {
	extra := ""
	if def != nil {
		extra = "DEFAULT_GENERATED"
	}
	return sqlmock.NewRows(showColumnsCols).AddRow("id", "char(36)", "NO", "PRI", def, extra)
}

func showTables(names ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"Tables_in_beads"})
	for _, n := range names {
		rows.AddRow(n)
	}
	return rows
}

func stubNoIssueRows(t *testing.T) {
	t.Helper()
	orig := issueRowCounter
	issueRowCounter = func(context.Context, DBConn) (int64, error) { return 0, nil }
	t.Cleanup(func() { issueRowCounter = orig })
}

// directDropDefault is the regexp for the post-check's own, unguarded
// correction of table.column.
func directDropDefault(table, column string) string {
	return "^" + regexp.QuoteMeta("ALTER TABLE `"+table+"` ALTER COLUMN `"+column+"` DROP DEFAULT") + "$"
}

func showColumnsOf(table string) string {
	return "^" + regexp.QuoteMeta("SHOW COLUMNS FROM "+table+" LIKE 'id'") + "$"
}

// GH#5269: a store carried the v50 mark while dependencies.id still had the
// DEFAULT (UUID()) that 0050 drops. Here 0050's body returns no error and
// changes nothing, and the post-check's direct DROP DEFAULT returns no error
// and changes nothing either. The mark must not be recorded: once it is, every
// later pass skips 0050 for good.
func TestRunMigrationsDoesNotRecordAMarkWhoseDroppedDefaultSurvives(t *testing.T) {
	stubNoIssueRows(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`ALTER TABLE dependencies ALTER COLUMN id DROP DEFAULT`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("issues", "dependencies"))
	mock.ExpectQuery(showColumnsOf("dependencies")).WillReturnRows(idDefault("(uuid())"))
	mock.ExpectExec(directDropDefault("dependencies", "id")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(showColumnsOf("dependencies")).WillReturnRows(idDefault("(uuid())"))

	applied, err := runMigrations(context.Background(), db, mainSource, 49, 50, false)
	if err == nil || !strings.Contains(err.Error(), "dependencies.id (DEFAULT (uuid())): default still present") {
		t.Fatalf("runMigrations(0050 with dependencies.id still DEFAULT (uuid())) = %d, %v; want a refusal naming dependencies.id and no schema_migrations row", applied, err)
	}
	if applied != 0 {
		t.Fatalf("applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestRunMigrationsRecordsTheMarkWhenTheDroppedDefaultIsGone(t *testing.T) {
	stubNoIssueRows(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`ALTER TABLE dependencies ALTER COLUMN id DROP DEFAULT`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("dependencies"))
	mock.ExpectQuery(showColumnsOf("dependencies")).WillReturnRows(idDefault(nil))
	mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(50, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	applied, err := runMigrations(context.Background(), db, mainSource, 49, 50, false)
	if err != nil || applied != 1 {
		t.Fatalf("runMigrations(0050, default gone) = %d, %v; want 1, nil", applied, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// AC-1: the body returned no error but left the default in place. The
// post-check drops it directly, reads it back as gone, and only then writes
// the cursor row.
func TestRunMigrationsCorrectsALeftoverDroppedDefaultBeforeTheMark(t *testing.T) {
	stubNoIssueRows(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`ALTER TABLE dependencies ALTER COLUMN id DROP DEFAULT`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("issues", "dependencies"))
	mock.ExpectQuery(showColumnsOf("dependencies")).WillReturnRows(idDefault("(uuid())"))
	mock.ExpectExec(directDropDefault("dependencies", "id")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(showColumnsOf("dependencies")).WillReturnRows(idDefault(nil))
	mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(50, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	applied, err := runMigrations(context.Background(), db, mainSource, 49, 50, false)
	if err != nil || applied != 1 {
		t.Fatalf("runMigrations(0050 leaving dependencies.id defaulted, correction works) = %d, %v; want 1, nil", applied, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations (want body -> probe -> ALTER -> re-probe -> INSERT): %v", err)
	}
}

// 0051 drops four defaults. A table the store lacks is skipped, as the body's
// guard skips it, and a default that survives its correction refuses the mark.
func TestRunMigrationsChecksEveryDefaultAMigrationDrops(t *testing.T) {
	stubNoIssueRows(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`ALTER TABLE events ALTER COLUMN id DROP DEFAULT`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("events", "comments"))
	mock.ExpectQuery(showColumnsOf("events")).WillReturnRows(idDefault(nil))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("events", "comments"))
	mock.ExpectQuery(showColumnsOf("comments")).WillReturnRows(idDefault("(uuid())"))
	mock.ExpectExec(directDropDefault("comments", "id")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(showColumnsOf("comments")).WillReturnRows(idDefault("(uuid())"))
	for i := 0; i < 2; i++ { // issue_snapshots, compaction_snapshots: absent
		mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("events", "comments"))
	}

	_, err = runMigrations(context.Background(), db, mainSource, 50, 51, false)
	if err == nil || !strings.Contains(err.Error(), "comments.id (DEFAULT (uuid()))") {
		t.Fatalf("runMigrations(0051 with comments.id still defaulted) err = %v, want a refusal naming comments.id", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}

	db2, mock2, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db2.Close()
	mock2.ExpectExec(`ALTER TABLE events ALTER COLUMN id DROP DEFAULT`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock2.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("events"))
	mock2.ExpectQuery(showColumnsOf("events")).WillReturnRows(idDefault(nil))
	for i := 0; i < 3; i++ { // comments, issue_snapshots, compaction_snapshots: absent
		mock2.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("events"))
	}
	mock2.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(51, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if applied, err := runMigrations(context.Background(), db2, mainSource, 50, 51, false); err != nil || applied != 1 {
		t.Fatalf("runMigrations(0051, absent tables skipped) = %d, %v; want 1, nil", applied, err)
	}
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// The check reads its columns from the frozen bodies, so it covers every
// shipped migration that drops a default. This pins what it finds.
func TestDroppedDefaultsOfTheShippedMigrations(t *testing.T) {
	want := map[string][]droppedDefault{
		"migrations/0043_drop_dependencies_generated_column.up.sql": {{"dependencies", "id"}},
		"migrations/0050_dependencies_deterministic_id.up.sql":      {{"dependencies", "id"}},
		"migrations/0051_drop_aux_id_defaults.up.sql": {
			{"events", "id"}, {"comments", "id"}, {"issue_snapshots", "id"}, {"compaction_snapshots", "id"},
		},
		"migrations/ignored/0010_drop_wisp_id_defaults.up.sql": {
			{"wisp_events", "id"}, {"wisp_comments", "id"}, {"wisp_dependencies", "id"},
		},
	}
	got := map[string][]droppedDefault{}
	for _, src := range []migrationSource{mainSource, ignoredSource} {
		for _, mf := range src.list() {
			path := src.dir + "/" + mf.name
			data, err := src.files.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			if d := droppedDefaults(string(data)); len(d) > 0 {
				got[path] = d
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("droppedDefaults over the shipped migrations = %v, want %v", fmt.Sprint(got), fmt.Sprint(want))
	}
}

// A statement quoted in a comment is prose, not an obligation; the guarded
// form carries the real statement inside a string literal, which must survive
// even when the literal itself contains comment markers.
func TestDroppedDefaultsIgnoresCommentsAndKeepsLiterals(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []droppedDefault
	}{
		{"line comment", "-- ALTER TABLE a ALTER COLUMN id DROP DEFAULT\nSELECT 1;", nil},
		{"hash comment", "# ALTER TABLE a ALTER COLUMN id DROP DEFAULT\nSELECT 1;", nil},
		{"block comment", "/* ALTER TABLE a ALTER COLUMN id\n DROP DEFAULT */ SELECT 1;", nil},
		{"guarded literal", "SET @sql = IF(@d, 'ALTER TABLE b ALTER COLUMN id DROP DEFAULT', 'SELECT 1');", []droppedDefault{{"b", "id"}}},
		{"literal holding comment markers", "SET @x = '-- not a comment /* nor this */'; SET @sql = 'ALTER TABLE `c` ALTER COLUMN `id` DROP DEFAULT';", []droppedDefault{{"c", "id"}}},
		{"escaped quote in literal", `SET @x = 'it''s -- still a literal'; ALTER TABLE d ALTER COLUMN id DROP DEFAULT;`, []droppedDefault{{"d", "id"}}},
		{"dedup by table and column", "ALTER TABLE E ALTER COLUMN ID DROP DEFAULT; ALTER TABLE e ALTER COLUMN id DROP DEFAULT;", []droppedDefault{{"e", "id"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := droppedDefaults(tc.sql); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("droppedDefaults(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}

// An empty-string default is a default; only SQL NULL means none. A LIKE
// lookalike (an underscore matches any character) is not the column.
func TestShowColumnDefaultNullVersusEmptyAndExactField(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(showColumnsOf("t")).WillReturnRows(sqlmock.NewRows(showColumnsCols).AddRow("id", "char(36)", "NO", "PRI", "", ""))
	got, err := showColumnDefault(context.Background(), db, "t", "id")
	if err != nil || !got.violated() || got.def.String != "" {
		t.Fatalf("empty-string default = %+v, %v; want present with a valid empty default", got, err)
	}
	mock.ExpectQuery(`^SHOW COLUMNS FROM t LIKE 'a_b'$`).WillReturnRows(sqlmock.NewRows(showColumnsCols).AddRow("axb", "char(36)", "NO", "", "x", ""))
	got, err = showColumnDefault(context.Background(), db, "t", "a_b")
	if err != nil || got.present {
		t.Fatalf("LIKE lookalike only = %+v, %v; want the column absent", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// AC-2: 0051 drops four defaults and every one of them goes wrong in its own
// way — the events probe fails, comments' correction fails, issue_snapshots'
// default survives its correction, and compaction_snapshots is corrected. The
// error names every condition that is not confirmed met with its observed
// default and cause, and the step writes neither its cursor row nor its step
// commit (sqlmock fails on any statement not expected here).
func TestRunMigrationsReportsEveryUnmetDroppedDefaultAndWritesNoMark(t *testing.T) {
	stubNoIssueRows(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	all := []string{"events", "comments", "issue_snapshots", "compaction_snapshots"}
	// commitEachStep: the step snapshots the working set first.
	mock.ExpectQuery(`(?s)SELECT s\.table_name, s\.staged\s+FROM dolt_status s`).
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}))
	mock.ExpectExec(`ALTER TABLE events ALTER COLUMN id DROP DEFAULT`).WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables(all...))
	mock.ExpectQuery(showColumnsOf("events")).WillReturnError(fmt.Errorf("connection reset"))

	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables(all...))
	mock.ExpectQuery(showColumnsOf("comments")).WillReturnRows(idDefault("(uuid())"))
	mock.ExpectExec(directDropDefault("comments", "id")).WillReturnError(fmt.Errorf("lock wait timeout"))

	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables(all...))
	mock.ExpectQuery(showColumnsOf("issue_snapshots")).WillReturnRows(idDefault("(uuid())"))
	mock.ExpectExec(directDropDefault("issue_snapshots", "id")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(showColumnsOf("issue_snapshots")).WillReturnRows(idDefault("(uuid())"))

	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables(all...))
	mock.ExpectQuery(showColumnsOf("compaction_snapshots")).WillReturnRows(idDefault("(uuid())"))
	mock.ExpectExec(directDropDefault("compaction_snapshots", "id")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(showColumnsOf("compaction_snapshots")).WillReturnRows(idDefault(nil))

	applied, err := runMigrations(context.Background(), db, mainSource, 50, 51, true)
	if err == nil {
		t.Fatalf("runMigrations(0051, three unmet conditions) = %d, nil; want a refusal", applied)
	}
	if applied != 0 {
		t.Fatalf("applied = %d, want 0", applied)
	}
	msg := err.Error()
	for _, want := range []string{
		"0051_drop_aux_id_defaults.up.sql",
		"events.id: probe failed",
		"connection reset",
		"comments.id (DEFAULT (uuid())): ALTER TABLE `comments` ALTER COLUMN `id` DROP DEFAULT failed",
		"lock wait timeout",
		"issue_snapshots.id (DEFAULT (uuid())): default still present",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "compaction_snapshots") {
		t.Errorf("error %q names compaction_snapshots, whose correction was verified", msg)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
