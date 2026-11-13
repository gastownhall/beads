package schema

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
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

// GH#5269: a store carried the v50 mark while dependencies.id still had the
// DEFAULT (UUID()) that 0050 drops. Here 0050's body returns no error and
// changes nothing, which is that state as runMigrations sees it. The pass must
// not record the mark while the default survives: the direct correction runs,
// and when it fails the step fails and nothing claims the version.
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
	mock.ExpectQuery(`^SHOW COLUMNS FROM dependencies LIKE 'id'$`).WillReturnRows(idDefault("(uuid())"))
	mock.ExpectExec("ALTER TABLE `dependencies` ALTER COLUMN `id` DROP DEFAULT").
		WillReturnError(errors.New("dolt: boom"))

	applied, err := runMigrations(context.Background(), db, mainSource, 49, 50, false)
	var perr *PostConditionError
	if err == nil || !errors.As(err, &perr) {
		t.Fatalf("runMigrations(0050 with dependencies.id still DEFAULT (uuid())) = %d, %v; want a *PostConditionError", applied, err)
	}
	if len(perr.Failures) != 1 || perr.Failures[0].Table != "dependencies" || perr.Failures[0].Column != "id" {
		t.Fatalf("failures = %+v, want one dependencies.id failure", perr.Failures)
	}
	if !strings.Contains(err.Error(), "dependencies.id") || !strings.Contains(err.Error(), "(uuid())") {
		t.Fatalf("err = %v, want it to name dependencies.id and the surviving default", err)
	}
	if applied != 0 {
		t.Fatalf("applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// A default the guarded body left behind is corrected directly and the mark is
// recorded only once the re-probe sees it gone (GH#5269).
func TestRunMigrationsCorrectsADefaultTheBodyLeftBehind(t *testing.T) {
	stubNoIssueRows(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`ALTER TABLE dependencies ALTER COLUMN id DROP DEFAULT`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("issues", "dependencies"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM dependencies LIKE 'id'$`).WillReturnRows(idDefault("(uuid())"))
	mock.ExpectExec("ALTER TABLE `dependencies` ALTER COLUMN `id` DROP DEFAULT").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`^SHOW COLUMNS FROM dependencies LIKE 'id'$`).WillReturnRows(idDefault(nil))
	mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(50, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	applied, err := runMigrations(context.Background(), db, mainSource, 49, 50, false)
	if err != nil || applied != 1 {
		t.Fatalf("runMigrations(0050 with a correctable default) = %d, %v; want 1, nil", applied, err)
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
	mock.ExpectQuery(`^SHOW COLUMNS FROM dependencies LIKE 'id'$`).WillReturnRows(idDefault(nil))
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

// 0051 drops four defaults. A table the store lacks is skipped, as the body's
// guard skips it, and a surviving default the correction cannot remove refuses
// the mark.
func TestRunMigrationsChecksEveryDefaultAMigrationDrops(t *testing.T) {
	stubNoIssueRows(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`ALTER TABLE events ALTER COLUMN id DROP DEFAULT`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("events", "comments"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM events LIKE 'id'$`).WillReturnRows(idDefault(nil))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("events", "comments"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM comments LIKE 'id'$`).WillReturnRows(idDefault("(uuid())"))
	mock.ExpectExec("ALTER TABLE `comments` ALTER COLUMN `id` DROP DEFAULT").
		WillReturnError(errors.New("dolt: boom"))

	_, err = runMigrations(context.Background(), db, mainSource, 50, 51, false)
	if err == nil || !strings.Contains(err.Error(), "comments.id") {
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
	mock2.ExpectQuery(`^SHOW COLUMNS FROM events LIKE 'id'$`).WillReturnRows(idDefault(nil))
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

// GH#5269, AC-2: a migration with several unmet conditions reports every one
// of them — the failed probe included — and writes neither the cursor row nor
// the step commit. The step runs with commitEachStep so the absence of
// DOLT_ADD/DOLT_COMMIT is observable.
func TestRunMigrationsPostCheckReportsEveryUnmetConditionAndWritesNothing(t *testing.T) {
	stubNoIssueRows(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// dirtyBeforeStep snapshot for the per-step commit path.
	mock.ExpectQuery(`FROM dolt_status`).WillReturnRows(
		sqlmock.NewRows([]string{"table_name", "staged"}))
	// 0051's body: the four guarded ALTERs in one Exec.
	mock.ExpectExec(`ALTER TABLE events ALTER COLUMN id DROP DEFAULT`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// Probes: events and comments carry their defaults, issue_snapshots'
	// column cannot be read, compaction_snapshots is absent (skipped).
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("events", "comments", "issue_snapshots"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM events LIKE 'id'$`).WillReturnRows(idDefault("(uuid())"))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("events", "comments", "issue_snapshots"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM comments LIKE 'id'$`).WillReturnRows(idDefault("(uuid())"))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("events", "comments", "issue_snapshots"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM issue_snapshots LIKE 'id'$`).WillReturnError(errors.New("dolt: read boom"))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("events", "comments", "issue_snapshots"))
	// Corrections: events' ALTER fails outright; comments' ALTER "succeeds"
	// but the re-probe still sees the default.
	mock.ExpectExec("ALTER TABLE `events` ALTER COLUMN `id` DROP DEFAULT").
		WillReturnError(errors.New("dolt: alter boom"))
	mock.ExpectExec("ALTER TABLE `comments` ALTER COLUMN `id` DROP DEFAULT").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`^SHOW COLUMNS FROM comments LIKE 'id'$`).WillReturnRows(idDefault("(uuid())"))

	applied, err := runMigrations(context.Background(), db, mainSource, 50, 51, true)
	var perr *PostConditionError
	if err == nil || !errors.As(err, &perr) {
		t.Fatalf("runMigrations(0051, three unmet conditions) = %d, %v; want a *PostConditionError", applied, err)
	}
	if applied != 0 {
		t.Fatalf("applied = %d, want 0", applied)
	}
	byColumn := map[string]PostConditionFailure{}
	for _, f := range perr.Failures {
		byColumn[f.Table+"."+f.Column] = f
	}
	if len(perr.Failures) != 3 {
		t.Fatalf("failures = %+v, want three (events, comments, issue_snapshots)", perr.Failures)
	}
	if f := byColumn["events.id"]; !f.Observed.Valid || f.Observed.String != "(uuid())" || !strings.Contains(f.Cause, "failed") {
		t.Fatalf("events.id failure = %+v, want the observed default and the failed ALTER", f)
	}
	if f := byColumn["comments.id"]; !f.Observed.Valid || !strings.Contains(f.Cause, "survived") {
		t.Fatalf("comments.id failure = %+v, want the surviving default after the correction", f)
	}
	if f := byColumn["issue_snapshots.id"]; f.Observed.Valid || !strings.Contains(f.Cause, "probe failed") {
		t.Fatalf("issue_snapshots.id failure = %+v, want a failed probe with no observed default", f)
	}
	for _, f := range perr.Failures {
		if f.Source != mainSource.cursorTable || f.Version != 51 {
			t.Fatalf("failure %+v lacks its source/version provenance", f)
		}
	}
	msg := err.Error()
	for _, want := range []string{"events.id", "comments.id", "issue_snapshots.id", "(uuid())", "GH#5269"} {
		if !strings.Contains(msg, want) {
			t.Errorf("err = %q, want it to identify %q", msg, want)
		}
	}
	// No cursor row and no step commit: the mock has no INSERT, DOLT_ADD or
	// DOLT_COMMIT expectation left, so any of them would surface here.
	if err := mock.ExpectationsWereMet(); err != nil {
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
