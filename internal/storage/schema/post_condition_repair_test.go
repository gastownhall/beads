package schema

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func obligationNames(t *testing.T, cursors map[string]int, retired map[droppedDefault]droppedDefaultRetirement) map[string][]string {
	t.Helper()
	obligations, err := obligationsAtCursors(cursors, retired)
	if err != nil {
		t.Fatalf("obligationsAtCursors(%v): %v", cursors, err)
	}
	out := map[string][]string{}
	for _, o := range obligations {
		out[o.d.String()] = o.sources
	}
	return out
}

// The recorded obligations follow each source's own cursor, deduplicate a
// column several migrations drop (keeping every source), and drop a column a
// recorded later migration retires (GH#5269).
func TestObligationsAtCursorsFollowsEachSourceAndRetirement(t *testing.T) {
	none := map[droppedDefault]droppedDefaultRetirement{}

	if got := obligationNames(t, map[string]int{"main": 42, "ignored": 9}, none); len(got) != 0 {
		t.Fatalf("below 0043 and ignored 0010: obligations %v, want none", got)
	}

	got := obligationNames(t, map[string]int{"main": 49, "ignored": 9}, none)
	want := map[string][]string{"dependencies.id": {"main 0043_drop_dependencies_generated_column.up.sql"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("main 49, ignored 9: %v, want %v", got, want)
	}

	got = obligationNames(t, map[string]int{"main": mainSource.latest(), "ignored": ignoredSource.latest()}, none)
	want = map[string][]string{
		"comments.id":             {"main 0051_drop_aux_id_defaults.up.sql"},
		"compaction_snapshots.id": {"main 0051_drop_aux_id_defaults.up.sql"},
		"dependencies.id": {
			"main 0043_drop_dependencies_generated_column.up.sql",
			"main 0050_dependencies_deterministic_id.up.sql",
		},
		"events.id":            {"main 0051_drop_aux_id_defaults.up.sql"},
		"issue_snapshots.id":   {"main 0051_drop_aux_id_defaults.up.sql"},
		"wisp_comments.id":     {"ignored 0010_drop_wisp_id_defaults.up.sql"},
		"wisp_dependencies.id": {"ignored 0010_drop_wisp_id_defaults.up.sql"},
		"wisp_events.id":       {"ignored 0010_drop_wisp_id_defaults.up.sql"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("at latest: %v, want %v", got, want)
	}

	retired := map[droppedDefault]droppedDefaultRetirement{{"comments", "id"}: {source: "main", version: 60}}
	if got := obligationNames(t, map[string]int{"main": 59, "ignored": 0}, retired); got["comments.id"] == nil {
		t.Fatalf("retirement at main 60 applied at cursor 59: %v", got)
	}
	if got := obligationNames(t, map[string]int{"main": 60, "ignored": 0}, retired); got["comments.id"] != nil {
		t.Fatalf("retirement at main 60 not applied at cursor 60: %v", got)
	}
}

// expectRecordedPostConditionCheck mocks CheckRecordedPostConditions on a
// store whose two cursors are at this binary's latest: each raw cursor read,
// then every recorded obligation's table listing and column read, in the
// check's own sorted order. defaults maps a "table.column" name to the
// default its probe reports; every other column reads as having none.
func expectRecordedPostConditionCheck(t *testing.T, mock sqlmock.Sqlmock, defaults map[string]string) {
	t.Helper()
	expectCursorProbe(mock, mainSource.cursorTable, true)
	expectScalar(mock, "SELECT MAX(version) FROM "+mainSource.cursorTable, "MAX(version)", LatestVersion())
	expectCursorProbe(mock, ignoredSource.cursorTable, true)
	expectScalar(mock, "SELECT MAX(version) FROM "+ignoredSource.cursorTable, "MAX(version)", LatestIgnoredVersion())
	obligations, err := obligationsAtCursors(map[string]int{"main": LatestVersion(), "ignored": LatestIgnoredVersion()}, retiredDroppedDefaults)
	if err != nil {
		t.Fatalf("obligationsAtCursors at latest: %v", err)
	}
	for _, o := range obligations {
		if o.d.column != "id" {
			t.Fatalf("obligation %s: this helper mocks id columns only", o.d)
		}
		var def driver.Value
		if v, ok := defaults[o.d.String()]; ok {
			def = v
		}
		expectShowTable(mock, o.d.table, true)
		mock.ExpectQuery(showColumnsOf(o.d.table)).WillReturnRows(idDefault(def))
	}
}

// newStatementRecordingMockConn is newMockConn whose mock also records every
// statement the code under test issues, in order — including one the ordered
// expectations reject. MigrateUpWithLock swallows a failed convergence probe
// and falls through to the locked path, so a probe that ran against the
// strict mock leaves the expectations met; only this record shows it ran.
func newStatementRecordingMockConn(t *testing.T) (*sql.Conn, sqlmock.Sqlmock, *[]string) {
	t.Helper()
	failOnSwallowedAdvisory(t)

	var statements []string
	recording := sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		statements = append(statements, actualSQL)
		return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(recording))
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin mock connection: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, mock, &statements
}

// Server-mode `bd migrate schema` reaches the GH#5269 repair through
// MigrateUpWithLock with WithPostConditionRepair. On a converged database the
// convergence fast path would return before the repair could look, so the
// option must skip it: the first statement is GET_LOCK (not the probe's
// SELECT DATABASE()), MigrateUp finds no work, and the repair reads every
// recorded obligation and drops the leftover default before RELEASE_LOCK. What
// it repaired lands in the caller's report. The mock is strict and ordered, so
// a repair skipped or run after the release fails here, and the statement
// record catches a probe that ran first. TestMigrateUpWithLockSkipsLockWhenAlreadyConverged
// pins the converged case without the option.
func TestMigrateUpWithLockPostConditionRepairRunsUnderTheLock(t *testing.T) {
	var notices bytes.Buffer
	saved := postConditionNoticeOut
	postConditionNoticeOut = &notices
	t.Cleanup(func() { postConditionNoticeOut = saved })

	conn, mock, statements := newStatementRecordingMockConn(t)

	lockName := MigrationLockName("testdb")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	// MigrateUp on a converged database: nothing to seed, untrack or apply.
	expectIgnorePatternSeedNoop(mock, LatestVersion())
	expectIgnoredCursorHealNoop(mock)
	expectNoMigrationWorkNeeded(mock)
	// The repair: wisp_events.id, a dolt-ignored table, still has the default
	// ignored 0010 drops. It is dropped and re-read; an ignored table is
	// neither staged nor committed.
	expectRecordedPostConditionCheck(t, mock, map[string]string{"wisp_events.id": "(uuid())"})
	mock.ExpectQuery(`(?s)SELECT t\.TABLE_NAME\s+FROM INFORMATION_SCHEMA\.TABLES t`).
		WillReturnRows(sqlmock.NewRows([]string{"TABLE_NAME"}).AddRow("dependencies").AddRow(mainSource.cursorTable))
	expectDoltStatusRows(mock)
	expectTransactionCommitSuspend(mock, 0)
	mock.ExpectExec(directDropDefault("wisp_events", "id")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(showColumnsOf("wisp_events")).WillReturnRows(idDefault(nil))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	var report PostConditionRepairReport
	applied, err := MigrateUpWithLock(context.Background(), conn, "testdb", WithPostConditionRepair(&report))
	if err != nil {
		t.Fatalf("MigrateUpWithLock(WithPostConditionRepair) error = %v", err)
	}
	if applied != 0 {
		t.Fatalf("applied = %d, want 0 on a converged database", applied)
	}
	if got, want := report.Names(), []string{"wisp_events.id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("report.Names() = %v, want %v", got, want)
	}
	if report.Committed {
		t.Fatalf("report.Committed = true, want no commit for an ignored table")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (GET_LOCK, MigrateUp, the repair, then RELEASE_LOCK): %v", err)
	}
	if len(*statements) == 0 || !strings.Contains((*statements)[0], "GET_LOCK") {
		first := "<none>"
		if len(*statements) > 0 {
			first = (*statements)[0]
		}
		t.Fatalf("first statement = %q, want GET_LOCK: WithPostConditionRepair must skip the convergence fast path", first)
	}
	// The repair names what it changed on the un-gated notice writer.
	if got := notices.String(); !strings.Contains(got, "Repaired wisp_events.id: dropped DEFAULT (uuid())") {
		t.Fatalf("repair notice = %q, want it to name wisp_events.id and the dropped default", got)
	}
}

// GH#5269: when staging or committing the repaired tracked tables fails, the
// dropped default is real but uncommitted, and a re-run cannot see it (the
// repair reads the working set's columns). So the repair must not call it
// repaired: the column moves to report.Uncommitted, the error is a
// *PostConditionRepairUncommittedError naming it and the commit that finishes
// it, and the "Repaired" notice is printed only for the dolt-ignored column,
// whose change needs no commit.
func TestRepairRecordedPostConditionsReportsAFailedCommitAsUncommitted_5269(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage error // DOLT_ADD's error; nil reaches the commit
	}{
		{name: "commit fails", stage: nil},
		{name: "staging fails", stage: errors.New("driver: bad connection")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var notices bytes.Buffer
			saved := postConditionNoticeOut
			postConditionNoticeOut = &notices
			t.Cleanup(func() { postConditionNoticeOut = saved })

			conn, mock, cleanup := newMockConn(t)
			defer cleanup()

			// dependencies is tracked; events (dolt-ignored since main 0062)
			// is not. Both still carry the default a recorded migration drops.
			expectRecordedPostConditionCheck(t, mock, map[string]string{"dependencies.id": "(uuid())", "events.id": "(uuid())"})
			mock.ExpectQuery(`(?s)SELECT t\.TABLE_NAME\s+FROM INFORMATION_SCHEMA\.TABLES t`).
				WillReturnRows(sqlmock.NewRows([]string{"TABLE_NAME"}).AddRow("dependencies").AddRow(mainSource.cursorTable))
			expectDoltStatusRows(mock)
			expectTransactionCommitSuspend(mock, 0)
			for _, table := range []string{"dependencies", "events"} {
				mock.ExpectExec(directDropDefault(table, "id")).WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectQuery(showColumnsOf(table)).WillReturnRows(idDefault(nil))
			}
			add := mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).WithArgs("dependencies")
			if tc.stage != nil {
				add.WillReturnError(tc.stage)
			} else {
				add.WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(0))
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
					WithArgs(postConditionRepairCommitMessage).
					WillReturnError(errors.New("i/o timeout"))
			}

			report, err := RepairRecordedPostConditions(context.Background(), conn)
			var uncommitted *PostConditionRepairUncommittedError
			if !errors.As(err, &uncommitted) {
				t.Fatalf("err = %T %v, want a *PostConditionRepairUncommittedError", err, err)
			}
			if want := []string{"dependencies.id"}; !reflect.DeepEqual(uncommitted.Columns, want) {
				t.Fatalf("error columns = %v, want %v", uncommitted.Columns, want)
			}
			if want := []string{"dependencies"}; !reflect.DeepEqual(uncommitted.Tables, want) {
				t.Fatalf("error tables = %v, want %v", uncommitted.Tables, want)
			}
			for _, want := range []string{"dependencies.id", "did not commit", "will not find it again", "bd vc status", "bd dolt commit", "CALL DOLT_COMMIT"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if got, want := report.Names(), []string{"events.id"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("report.Names() = %v, want %v: only the ignored column's repair is finished", got, want)
			}
			if got, want := report.UncommittedNames(), []string{"dependencies.id"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("report.UncommittedNames() = %v, want %v", got, want)
			}
			if report.Committed {
				t.Fatalf("report.Committed = true after a failed commit")
			}
			if got := notices.String(); !strings.Contains(got, "Repaired events.id") || strings.Contains(got, "Repaired dependencies.id") {
				t.Fatalf("notices = %q, want a Repaired line for events.id only", got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// merge folds retry attempts together: repairs accumulate, and a column stays
// uncommitted until an attempt finishes it.
func TestPostConditionRepairReportMergeKeepsUncommittedUntilFinished(t *testing.T) {
	v := func(table string) PostConditionViolation { return PostConditionViolation{Table: table, Column: "id"} }
	var r PostConditionRepairReport
	r.merge(PostConditionRepairReport{Repaired: []PostConditionViolation{v("events")}, Uncommitted: []PostConditionViolation{v("dependencies"), v("comments")}})
	r.merge(PostConditionRepairReport{Repaired: []PostConditionViolation{v("comments")}, Committed: true})
	if got, want := r.Names(), []string{"comments.id", "events.id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	if got, want := r.UncommittedNames(), []string{"dependencies.id"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("UncommittedNames() = %v, want %v (comments.id was finished by the later attempt)", got, want)
	}
	if !r.Committed {
		t.Fatalf("Committed = false, want true once an attempt committed")
	}
}
