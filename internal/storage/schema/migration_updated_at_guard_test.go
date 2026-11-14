package schema

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const (
	// The pre-repairs (ensureWispIsBlockedForRecompute and kin) still probe
	// through INFORMATION_SCHEMA; the guard itself never does.
	guardRepairColumnQuery = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS.*TABLE_NAME = \? AND COLUMN_NAME = \?`
	guardTableQuery        = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES.*TABLE_NAME = \?`
	guardIndexQuery        = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.STATISTICS.*TABLE_NAME = \? AND INDEX_NAME = \?`
)

// quietStderr discards the runner's progress lines for one test.
func quietStderr(t *testing.T) {
	t.Helper()
	orig := stderr
	stderr = &bytes.Buffer{}
	t.Cleanup(func() { stderr = orig })
}

// stubUpdatedAtGuard turns the runner's updated_at snapshot into a no-op for
// runner fakes (mockDB and kin) that cannot answer its queries, the way those
// tests stub issueRowCounter. Tests of the guard itself never use it.
func stubUpdatedAtGuard(t *testing.T) {
	t.Helper()
	orig := snapshotUpdatedAtForStep
	snapshotUpdatedAtForStep = func(context.Context, DBConn, migrationSource, int) (*updatedAtSnapshot, error) {
		return nil, nil
	}
	t.Cleanup(func() { snapshotUpdatedAtForStep = orig })
}

// TestRunMigrationsRestoresIgnored0007UpdatedAtBeforeItsCursorRow is the
// runner half of AC-4: on the ignored source (no per-step commit), ignored
// 0007's guard snapshots wisps before its pre-repair, and restores the row
// its body stamped before the cursor row is recorded. sqlmock matches calls
// in order, so the expectation sequence is the assertion.
func TestRunMigrationsRestoresIgnored0007UpdatedAtBeforeItsCursorRow(t *testing.T) {
	failOnSwallowedAdvisory(t)
	quietStderr(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// 1. Snapshot, with the is_blocked flag the body rewrites.
	guardExpectColumns(mock, "wisps", "id", "updated_at", "is_blocked")
	mock.ExpectQuery(`SELECT id, updated_at, is_blocked FROM wisps ORDER BY id`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at", "is_blocked"}).
			AddRow("w-1", "2025-04-02 09:30:32", 0).
			AddRow("w-2", "2025-04-03 09:30:33", 0))
	// 2. ensureWispIsBlockedForRecompute: everything already in place.
	mock.ExpectQuery(guardTableQuery).WithArgs("wisps").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(guardRepairColumnQuery).WithArgs("wisps", "is_blocked").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(guardIndexQuery).WithArgs("wisps", "idx_wisps_is_blocked").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	// 3. The frozen body.
	mock.ExpectExec(`UPDATE wisps SET is_blocked = 0`).WillReturnResult(sqlmock.NewResult(0, 1))
	// 4. Restore: only w-1 was rewritten (now blocked) and stamped; w-2 is
	// unchanged and not rewritten.
	guardExpectColumns(mock, "wisps", "id", "updated_at", "is_blocked")
	mock.ExpectQuery(`SELECT id, updated_at, is_blocked FROM wisps ORDER BY id`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at", "is_blocked"}).
			AddRow("w-1", "2026-11-02 04:00:00", 1).
			AddRow("w-2", "2025-04-03 09:30:33", 0))
	mock.ExpectExec(guardRestoreSQL("wisps", 1)).
		WithArgs(guardRestoreArgs(guardRestoreRow{"w-1", "2025-04-02 09:30:32", "2026-11-02 04:00:00"})...).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// 5. Only now the cursor row; no per-step commit on this source.
	mock.ExpectExec(`INSERT IGNORE INTO ignored_schema_migrations`).WithArgs(7, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	applied, err := runMigrations(context.Background(), db, ignoredSource, 6, 7, false)
	if err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations (ordering mismatch would surface here): %v", err)
	}
}

// TestRunMigrationsFailsGuardedStepOnUpdatedAtGuardError pins AC-5: on the
// committing main path, a failed snapshot or restoration of a registered
// step (main 0046) returns that error before the step's cursor row, its
// DOLT_COMMIT or its "done" line. sqlmock answers any statement after the
// injected failure with an unexpected-call error of its own, so errors.Is on
// the injected error proves the runner stopped right there.
func TestRunMigrationsFailsGuardedStepOnUpdatedAtGuardError(t *testing.T) {
	dirtyStatusQuery := `(?s)SELECT s\.table_name, s\.staged\s+FROM dolt_status s\s+WHERE NOT EXISTS`
	injected := errors.New("injected updated_at guard failure")

	for _, tc := range []struct {
		name   string
		expect func(mock sqlmock.Sqlmock)
		want   string
	}{
		{
			name: "snapshot column listing fails",
			expect: func(mock sqlmock.Sqlmock) {
				guardExpectShowTables(mock, "issues")
				mock.ExpectQuery(guardShowColumnsQuery("issues")).WillReturnError(injected)
			},
			want: "snapshotting updated_at before migration 0046_",
		},
		{
			name: "snapshot read fails",
			expect: func(mock sqlmock.Sqlmock) {
				guardExpectColumns(mock, "issues", "id", "updated_at")
				mock.ExpectQuery(guardIssuesSelect).WillReturnError(injected)
			},
			want: "snapshotting updated_at before migration 0046_",
		},
		{
			name: "post-body reread fails",
			expect: func(mock sqlmock.Sqlmock) {
				guardPrimeThroughBody(mock, false, false, oneGuardRow(guardOldStamp))
				mock.ExpectQuery(guardIssuesFlagSelect).WillReturnError(injected)
			},
			want: "restoring updated_at after migration 0046_",
		},
		{
			name: "restore write fails",
			expect: func(mock sqlmock.Sqlmock) {
				guardPrimeThroughBody(mock, false, false, oneGuardRow(guardOldStamp))
				mock.ExpectQuery(guardIssuesFlagSelect).WillReturnRows(oneGuardFlagRow(guardNewStamp, 1))
				mock.ExpectExec(`UPDATE issues SET updated_at = CASE id`).WillReturnError(injected)
			},
			want: "restoring updated_at after migration 0046_",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failOnSwallowedAdvisory(t)
			var out bytes.Buffer
			orig := stderr
			stderr = &out
			t.Cleanup(func() { stderr = orig })
			origCounter := issueRowCounter
			issueRowCounter = func(context.Context, DBConn) (int64, error) { return 0, nil }
			t.Cleanup(func() { issueRowCounter = origCounter })
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectQuery(dirtyStatusQuery).
				WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}))
			tc.expect(mock)

			applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, true)
			if !errors.Is(err, injected) {
				t.Fatalf("runMigrations err = %v, want the injected guard failure", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("runMigrations err = %q, want it to name %q", err, tc.want)
			}
			if applied != 0 {
				t.Errorf("applied = %d, want 0: the failed step must not count", applied)
			}
			if strings.Contains(out.String(), "done") {
				t.Errorf("stderr reports the failed step done:\n%s", out.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

// TestUpdatedAtGuardIssuesNoQueriesOutsideTheRegistry pins AC-6: a step
// outside updatedAtGuards gets no snapshot, reread or restoration query. A
// sqlmock with no expectations fails any statement, so a nil snapshot and nil
// error for every unregistered version of both series proves none was sent.
func TestUpdatedAtGuardIssuesNoQueriesOutsideTheRegistry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	unregistered := 0
	for _, src := range []migrationSource{mainSource, ignoredSource} {
		for _, mf := range src.list() {
			if _, ok := updatedAtGuards[repairKey{src.cursorTable, mf.version}]; ok {
				continue
			}
			unregistered++
			snap, err := snapshotUpdatedAt(context.Background(), db, src, mf.version)
			if err != nil || snap != nil {
				t.Errorf("snapshotUpdatedAt(%s, %d) = %v, %v; want nil, nil", src.cursorTable, mf.version, snap, err)
			}
			if err := snap.restore(context.Background(), db); err != nil {
				t.Errorf("restore after %s/%d = %v, want a no-op", src.cursorTable, mf.version, err)
			}
		}
	}
	if unregistered == 0 {
		t.Fatal("no unregistered migration found; the sweep tested nothing")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRunMigrationsRunsUnregisteredStepWithoutGuardQueries is AC-6 through
// the runner: main 0048 (unregistered) runs its body and records its cursor
// row with nothing in between or before.
func TestRunMigrationsRunsUnregisteredStepWithoutGuardQueries(t *testing.T) {
	failOnSwallowedAdvisory(t)
	quietStderr(t)
	origCounter := issueRowCounter
	issueRowCounter = func(context.Context, DBConn) (int64, error) { return 0, nil }
	t.Cleanup(func() { issueRowCounter = origCounter })
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`ALTER TABLE events MODIFY old_value LONGTEXT`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(48, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	applied, err := runMigrations(context.Background(), db, mainSource, 47, 48, false)
	if err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// zeroIssueRowCounter stubs the main pass's large-rig row count, which sqlmock
// fakes do not answer.
func zeroIssueRowCounter(t *testing.T) {
	t.Helper()
	orig := issueRowCounter
	issueRowCounter = func(context.Context, DBConn) (int64, error) { return 0, nil }
	t.Cleanup(func() { issueRowCounter = orig })
}

const (
	guardDirtyStatusQuery = `(?s)SELECT s\.table_name, s\.staged\s+FROM dolt_status s\s+WHERE NOT EXISTS`
	guardIssuesSelect     = `SELECT id, updated_at FROM issues ORDER BY id`
	guardIssuesFlagSelect = `SELECT id, updated_at, is_blocked FROM issues ORDER BY id`
	guardOldStamp         = "2025-03-01 09:30:00"
	guardNewStamp         = "2026-11-02 04:00:00"
)

// guardShowColumnsQuery matches guardColumnsOf's listing of table.
func guardShowColumnsQuery(table string) string {
	return `^SHOW COLUMNS FROM ` + table + `$`
}

// guardExpectShowTables primes guardColumnsOf's SHOW TABLES, listing tables.
func guardExpectShowTables(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"Tables_in_beads"})
	for _, table := range tables {
		rows.AddRow(table)
	}
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(rows)
}

// guardShowColumnsRows is a SHOW COLUMNS result listing cols, in order.
func guardShowColumnsRows(cols ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"})
	for _, col := range cols {
		rows.AddRow(col, "text", "YES", "", nil, "")
	}
	return rows
}

// guardExpectColumns primes one guardColumnsOf listing of a present table:
// SHOW TABLES finds it, SHOW COLUMNS lists cols in table order.
func guardExpectColumns(mock sqlmock.Sqlmock, table string, cols ...string) {
	guardExpectShowTables(mock, "dependencies", table)
	mock.ExpectQuery(guardShowColumnsQuery(table)).WillReturnRows(guardShowColumnsRows(cols...))
}

// guardExpectAbsentTable primes one guardColumnsOf probe of a table the
// database lacks: SHOW TABLES does not list it and no SHOW COLUMNS follows.
func guardExpectAbsentTable(mock sqlmock.Sqlmock) {
	guardExpectShowTables(mock, "dependencies", "issues")
}

// guardRestoreSQL matches the statement restoreUpdatedAtBatch builds for n
// rows of table: the rows picked by an id IN list (a primary-key lookup), not
// an OR of per-row conditions (a full table scan).
func guardRestoreSQL(table string, n int) string {
	cases := strings.Repeat(` WHEN \? THEN \?`, n)
	return `^UPDATE ` + table + ` SET updated_at = CASE id` + cases + ` ELSE updated_at END` +
		` WHERE id IN \(\?` + strings.Repeat(`, \?`, n-1) + `\)` +
		` AND updated_at = CASE id` + cases + ` END$`
}

// guardRestoreRow is one restored row: its id, the value written back, and
// the value the post-body reread saw.
type guardRestoreRow struct{ id, want, got string }

// guardRestoreArgs returns restoreUpdatedAtBatch's arguments for rows.
func guardRestoreArgs(rows ...guardRestoreRow) []driver.Value {
	args := make([]driver.Value, 0, 5*len(rows))
	for _, r := range rows {
		args = append(args, r.id, r.want)
	}
	for _, r := range rows {
		args = append(args, r.id)
	}
	for _, r := range rows {
		args = append(args, r.id, r.got)
	}
	return args
}

// oneGuardRow is a one-issue snapshot taken before the issues table had its
// is_blocked flag (v45).
func oneGuardRow(stamp string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "updated_at"}).AddRow("bd-1", stamp)
}

// oneGuardFlagRow is a one-issue read with its is_blocked flag.
func oneGuardFlagRow(stamp string, blocked int) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "updated_at", "is_blocked"}).AddRow("bd-1", stamp, blocked)
}

// guardPrimeThroughBody primes main 0046 from v45 up to and including the
// restore's column probes: the per-step dirty-table snapshot (commitEachStep
// only), the guard's snapshot of snapshotRows (with is_blocked when
// flagBefore, as on a store that already has the column), and the frozen
// body (0046 has no pre-repair). The caller primes the post-body reread
// (guardIssuesFlagSelect) and what follows.
func guardPrimeThroughBody(mock sqlmock.Sqlmock, commitEachStep, flagBefore bool, snapshotRows *sqlmock.Rows) {
	if commitEachStep {
		mock.ExpectQuery(guardDirtyStatusQuery).
			WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}))
	}
	if flagBefore {
		guardExpectColumns(mock, "issues", "id", "updated_at", "is_blocked")
	} else {
		guardExpectColumns(mock, "issues", "id", "updated_at")
	}
	if flagBefore {
		mock.ExpectQuery(guardIssuesFlagSelect).WillReturnRows(snapshotRows)
	} else {
		mock.ExpectQuery(guardIssuesSelect).WillReturnRows(snapshotRows)
	}
	mock.ExpectExec(`SET @needs_add`).WillReturnResult(sqlmock.NewResult(0, 1))
	guardExpectColumns(mock, "issues", "id", "updated_at", "is_blocked")
}

// guardPrimeTail primes main 0046's cursor row and, on the commitEachStep
// path, the step's commit of issues plus the cursor table.
func guardPrimeTail(mock sqlmock.Sqlmock, commitEachStep bool) {
	mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(46, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if commitEachStep {
		mock.ExpectQuery(guardDirtyStatusQuery).
			WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}).AddRow("issues", false))
		mock.ExpectQuery(`CALL DOLT_ADD\('-f', \?\)`).WithArgs("issues").
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
		mock.ExpectQuery(`CALL DOLT_ADD\('-f', \?\)`).WithArgs("schema_migrations").
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
		mock.ExpectQuery(`CALL DOLT_COMMIT`).
			WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	}
}

// TestUpdatedAtGuardRestoresTimestampsAroundRegisteredStep is AC-1's runner
// half: on a registered step (main 0046) the restore runs between a
// successful body and the cursor row, on both the per-step-commit and the
// bounded path; rows the body left alone are not rewritten; a row whose flag
// the body never touched keeps a timestamp another writer gave it during the
// step; a row the recompute cleared and set again, or cleared for good, is
// restored; a compare-and-swap miss does not fail the step; and more than
// restoreBatchSize stamped rows split into bounded statements. sqlmock
// matches calls in order, so the expectation sequence is the assertion.
func TestUpdatedAtGuardRestoresTimestampsAroundRegisteredStep(t *testing.T) {
	run := func(t *testing.T, commitEachStep bool, prime func(mock sqlmock.Sqlmock)) {
		t.Helper()
		failOnSwallowedAdvisory(t)
		quietStderr(t)
		zeroIssueRowCounter(t)
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		prime(mock)
		applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, commitEachStep)
		if err != nil {
			t.Fatalf("runMigrations: %v", err)
		}
		if applied != 1 {
			t.Fatalf("applied = %d, want 1", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations (statement order would surface here): %v", err)
		}
	}

	for _, commitEachStep := range []bool{true, false} {
		name := "restore precedes cursor row and step commit"
		if !commitEachStep {
			name = "bounded pass restores the same way"
		}
		t.Run(name, func(t *testing.T) {
			run(t, commitEachStep, func(mock sqlmock.Sqlmock) {
				guardPrimeThroughBody(mock, commitEachStep, false, oneGuardRow(guardOldStamp))
				mock.ExpectQuery(guardIssuesFlagSelect).WillReturnRows(oneGuardFlagRow(guardNewStamp, 1))
				mock.ExpectExec(guardRestoreSQL("issues", 1)).
					WithArgs(guardRestoreArgs(guardRestoreRow{"bd-1", guardOldStamp, guardNewStamp})...).
					WillReturnResult(sqlmock.NewResult(0, 1))
				guardPrimeTail(mock, commitEachStep)
			})
		})
	}

	t.Run("rows the body left alone are not rewritten", func(t *testing.T) {
		run(t, false, func(mock sqlmock.Sqlmock) {
			guardPrimeThroughBody(mock, false, false, oneGuardRow(guardOldStamp))
			// No restore is primed: an issued UPDATE fails as unexpected.
			mock.ExpectQuery(guardIssuesFlagSelect).WillReturnRows(oneGuardFlagRow(guardOldStamp, 0))
			guardPrimeTail(mock, false)
		})
	})

	t.Run("a row whose flag the body never set keeps another writer's updated_at", func(t *testing.T) {
		run(t, false, func(mock sqlmock.Sqlmock) {
			guardPrimeThroughBody(mock, false, true, oneGuardFlagRow(guardOldStamp, 0))
			// Another session edited bd-1 during the step; the recompute
			// left it unblocked, so the body never wrote it. No restore is
			// primed: an issued UPDATE fails as unexpected.
			mock.ExpectQuery(guardIssuesFlagSelect).WillReturnRows(oneGuardFlagRow("2026-11-02 03:59:58", 0))
			guardPrimeTail(mock, false)
		})
	})

	t.Run("rows the recompute cleared are restored", func(t *testing.T) {
		run(t, false, func(mock sqlmock.Sqlmock) {
			// bd-1 stays blocked: the body cleared it and set it again, so
			// its flag reads the same before and after. bd-2 is unblocked
			// for good. The clear stamped both.
			guardPrimeThroughBody(mock, false, true, sqlmock.NewRows([]string{"id", "updated_at", "is_blocked"}).
				AddRow("bd-1", guardOldStamp, 1).
				AddRow("bd-2", "2025-03-02 10:00:00", 1))
			mock.ExpectQuery(guardIssuesFlagSelect).WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at", "is_blocked"}).
				AddRow("bd-1", guardNewStamp, 1).
				AddRow("bd-2", guardNewStamp, 0))
			mock.ExpectExec(guardRestoreSQL("issues", 2)).
				WithArgs(guardRestoreArgs(
					guardRestoreRow{"bd-1", guardOldStamp, guardNewStamp},
					guardRestoreRow{"bd-2", "2025-03-02 10:00:00", guardNewStamp},
				)...).
				WillReturnResult(sqlmock.NewResult(0, 2))
			guardPrimeTail(mock, false)
		})
	})

	t.Run("a compare-and-swap miss is not an error", func(t *testing.T) {
		run(t, false, func(mock sqlmock.Sqlmock) {
			guardPrimeThroughBody(mock, false, false, oneGuardRow(guardOldStamp))
			mock.ExpectQuery(guardIssuesFlagSelect).WillReturnRows(oneGuardFlagRow(guardNewStamp, 1))
			// Another writer moved the row after the reread: the predicate
			// matches nothing and the step still records.
			mock.ExpectExec(guardRestoreSQL("issues", 1)).
				WithArgs(guardRestoreArgs(guardRestoreRow{"bd-1", guardOldStamp, guardNewStamp})...).
				WillReturnResult(sqlmock.NewResult(0, 0))
			guardPrimeTail(mock, false)
		})
	})

	t.Run("rows past the batch boundary split into bounded statements", func(t *testing.T) {
		n := restoreBatchSize + 1
		id := func(i int) string { return fmt.Sprintf("bd-%04d", i) }
		before := sqlmock.NewRows([]string{"id", "updated_at"})
		after := sqlmock.NewRows([]string{"id", "updated_at", "is_blocked"})
		var full []guardRestoreRow
		for i := range n {
			before.AddRow(id(i), guardOldStamp)
			after.AddRow(id(i), guardNewStamp, 1)
			if i < restoreBatchSize {
				full = append(full, guardRestoreRow{id(i), guardOldStamp, guardNewStamp})
			}
		}
		run(t, false, func(mock sqlmock.Sqlmock) {
			guardPrimeThroughBody(mock, false, false, before)
			mock.ExpectQuery(guardIssuesFlagSelect).WillReturnRows(after)
			mock.ExpectExec(guardRestoreSQL("issues", restoreBatchSize)).WithArgs(guardRestoreArgs(full...)...).
				WillReturnResult(sqlmock.NewResult(0, int64(restoreBatchSize)))
			mock.ExpectExec(guardRestoreSQL("issues", 1)).
				WithArgs(guardRestoreArgs(guardRestoreRow{id(n - 1), guardOldStamp, guardNewStamp})...).
				WillReturnResult(sqlmock.NewResult(0, 1))
			guardPrimeTail(mock, false)
		})
	})
}

// TestUpdatedAtGuardRowMovesPreferDestinationHistory is AC-3's statement-level
// half for main 0035: a moved row takes its issues (source) value, a row the
// wisps destination already held keeps its own value over a same-ID source
// value, a destination row whose ephemeral flag the body flipped (from 0 or
// NULL) is restored, a destination row whose flag the body left alone keeps
// a timestamp another writer gave it during the step, and rows the step
// created (no snapshot) or deleted (absent from the reread) are left alone.
func TestUpdatedAtGuardRowMovesPreferDestinationHistory(t *testing.T) {
	failOnSwallowedAdvisory(t)
	quietStderr(t)
	zeroIssueRowCounter(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Snapshot, sorted by table: issues (source, no flag) then wisps
	// (destination, with the ephemeral flag the body sets).
	guardExpectColumns(mock, "issues", "id", "updated_at", "ephemeral")
	mock.ExpectQuery(guardIssuesSelect).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).
			AddRow("mover", "2025-01-06 10:00:00").
			AddRow("native", "2025-01-01 00:00:00"))
	guardExpectColumns(mock, "wisps", "id", "updated_at", "ephemeral")
	mock.ExpectQuery(`SELECT id, updated_at, ephemeral FROM wisps ORDER BY id`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at", "ephemeral"}).
			AddRow("bystander", "2025-01-03 00:00:00", 1).
			AddRow("doomed", "2025-01-02 00:00:00", 0).
			AddRow("legacy", "2025-01-04 00:00:00", nil).
			AddRow("native", "2025-02-10 08:00:00", 0))
	// 0035's body (no pre-repair at 35).
	mock.ExpectExec(`SET @shared_cols`).WillReturnResult(sqlmock.NewResult(0, 0))
	// Reread: fresh is new, mover arrived stamped, legacy and native
	// (collision) got flipped and stamped, bystander was edited by another
	// writer with its flag already set, doomed is gone. Only mover, legacy
	// and native are restored.
	guardExpectColumns(mock, "wisps", "id", "updated_at", "ephemeral")
	mock.ExpectQuery(`SELECT id, updated_at, ephemeral FROM wisps ORDER BY id`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at", "ephemeral"}).
			AddRow("bystander", "2026-11-02 03:59:58", 1).
			AddRow("fresh", guardNewStamp, 1).
			AddRow("legacy", guardNewStamp, 1).
			AddRow("mover", guardNewStamp, 1).
			AddRow("native", guardNewStamp, 1))
	mock.ExpectExec(guardRestoreSQL("wisps", 3)).
		WithArgs(guardRestoreArgs(
			guardRestoreRow{"legacy", "2025-01-04 00:00:00", guardNewStamp},
			guardRestoreRow{"mover", "2025-01-06 10:00:00", guardNewStamp},
			guardRestoreRow{"native", "2025-02-10 08:00:00", guardNewStamp},
		)...).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(35, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if _, err := runMigrations(context.Background(), db, mainSource, 34, 35, false); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations (lookup precedence or skip rules wrong): %v", err)
	}
}

// TestUpdatedAtGuardRestoresJoinStampedRowsOnly pins the join rule on main
// 0041, whose dependency-split UPDATEs join issues and wisps without
// assigning them: a row whose columns other than updated_at read the same
// after the step was stamped by the join and gets its pre-step value back; a
// row whose other columns changed was edited by another writer and keeps
// its time; an untouched row is not rewritten; and when the step drops a
// fingerprinted column the rule is off, so nothing is restored on a guess.
func TestUpdatedAtGuardRestoresJoinStampedRowsOnly(t *testing.T) {
	fpSelect := "SELECT id, updated_at, " + regexp.QuoteMeta(fingerprintSQL([]string{"id", "title", "status"})) + " FROM issues ORDER BY id"
	snapshot := func(mock sqlmock.Sqlmock) {
		guardExpectColumns(mock, "issues", "id", "title", "updated_at", "status")
		mock.ExpectQuery(fpSelect).WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at", "fp"}).
			AddRow("bd-edited", "2025-03-02 10:00:00", "fp-edited").
			AddRow("bd-target", guardOldStamp, "fp-target").
			AddRow("bd-untouched", "2025-03-03 11:00:00", "fp-untouched"))
		guardExpectAbsentTable(mock)
	}
	afterBody := sqlmock.NewRows([]string{"id", "updated_at", "fp"}).
		AddRow("bd-edited", "2026-11-02 03:59:58", "fp-edited-by-another-writer").
		AddRow("bd-target", guardNewStamp, "fp-target").
		AddRow("bd-untouched", "2025-03-03 11:00:00", "fp-untouched")

	run := func(t *testing.T, expect func(mock sqlmock.Sqlmock)) {
		t.Helper()
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		expect(mock)
		snap, err := snapshotUpdatedAt(context.Background(), db, mainSource, 41)
		if err != nil {
			t.Fatalf("snapshotUpdatedAt: %v", err)
		}
		if err := snap.restore(context.Background(), db); err != nil {
			t.Fatalf("restore: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	}

	t.Run("join-stamped row restored, edited and untouched rows left alone", func(t *testing.T) {
		run(t, func(mock sqlmock.Sqlmock) {
			snapshot(mock)
			guardExpectColumns(mock, "issues", "id", "title", "updated_at", "status")
			mock.ExpectQuery(fpSelect).WillReturnRows(afterBody)
			mock.ExpectExec(guardRestoreSQL("issues", 1)).
				WithArgs(guardRestoreArgs(guardRestoreRow{"bd-target", guardOldStamp, guardNewStamp})...).
				WillReturnResult(sqlmock.NewResult(0, 1))
			guardExpectAbsentTable(mock)
		})
	})

	t.Run("a dropped fingerprint column turns the join rule off", func(t *testing.T) {
		run(t, func(mock sqlmock.Sqlmock) {
			snapshot(mock)
			guardExpectColumns(mock, "issues", "id", "title", "updated_at")
			// No fingerprint is read and no restore is primed: an issued
			// UPDATE fails as unexpected.
			mock.ExpectQuery(`SELECT id, updated_at FROM issues ORDER BY id`).
				WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).AddRow("bd-target", guardNewStamp))
			guardExpectAbsentTable(mock)
		})
	})
}

// TestUpdatedAtGuardProbesColumnsWithoutInformationSchema pins the guard's
// probe form for every registered step of both series: snapshot and restore
// ask only SHOW TABLES, SHOW COLUMNS FROM <table> and their own row reads,
// never INFORMATION_SCHEMA. Dolt does not push an INFORMATION_SCHEMA.COLUMNS
// predicate down, so each such probe walks every database on the server
// (~6 s at 300 databases on Dolt 2.6.0) while the migration lock is held.
// The matcher records every statement the guard sends and refuses any that
// names INFORMATION_SCHEMA, whichever expectation it is offered to.
func TestUpdatedAtGuardProbesColumnsWithoutInformationSchema(t *testing.T) {
	allowed := regexp.MustCompile(`^(SHOW TABLES|SHOW COLUMNS FROM (issues|wisps)|SELECT id, updated_at\b.* FROM (issues|wisps) ORDER BY id)$`)
	for key := range updatedAtGuards {
		t.Run(fmt.Sprintf("%s/%d", key.cursorTable, key.version), func(t *testing.T) {
			sent := map[string]bool{}
			matcher := sqlmock.QueryMatcherFunc(func(expected, actual string) error {
				sent[actual] = true
				if strings.Contains(strings.ToLower(actual), "information_schema") {
					return fmt.Errorf("guard sent an INFORMATION_SCHEMA probe: %s", actual)
				}
				re, err := regexp.Compile(expected)
				if err != nil {
					return err
				}
				if !re.MatchString(actual) {
					return fmt.Errorf("%q does not match %q", actual, expected)
				}
				return nil
			})
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			mock.MatchExpectationsInOrder(false)
			cols := []string{"id", "title", "updated_at", "is_blocked", "ephemeral"}
			for range 8 {
				guardExpectShowTables(mock, "issues", "wisps")
				for _, table := range []string{"issues", "wisps"} {
					mock.ExpectQuery(guardShowColumnsQuery(table)).WillReturnRows(guardShowColumnsRows(cols...))
					mock.ExpectQuery(`^SELECT id, updated_at\b.* FROM ` + table + ` ORDER BY id$`).
						WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}))
				}
			}

			src := mainSource
			if key.cursorTable == ignoredSource.cursorTable {
				src = ignoredSource
			}
			snap, err := snapshotUpdatedAt(context.Background(), db, src, key.version)
			if err != nil {
				t.Fatalf("snapshotUpdatedAt: %v", err)
			}
			if snap == nil {
				t.Fatal("registered step got no snapshot")
			}
			if err := snap.restore(context.Background(), db); err != nil {
				t.Fatalf("restore: %v", err)
			}
			if len(sent) == 0 {
				t.Fatal("guard sent no statement; the sweep tested nothing")
			}
			for query := range sent {
				if !allowed.MatchString(query) {
					t.Errorf("guard sent %q; want only SHOW TABLES, SHOW COLUMNS and its row reads", query)
				}
			}
		})
	}
}

// TestUpdatedAtGuardColumnNamesCompareCaseInsensitively pins that the
// SHOW-based listing answers as the INFORMATION_SCHEMA probes it replaced:
// a table SHOW TABLES lists in another case is present, updated_at and the
// flag are found in any case, and the fingerprint drops updated_at in any
// case while keeping table order.
func TestUpdatedAtGuardColumnNamesCompareCaseInsensitively(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	guardExpectShowTables(mock, "dependencies", "WISPS")
	mock.ExpectQuery(guardShowColumnsQuery("wisps")).
		WillReturnRows(guardShowColumnsRows("ID", "Title", "Updated_At", "IS_BLOCKED"))

	cols, err := guardColumnsOf(context.Background(), db, "wisps")
	if err != nil {
		t.Fatalf("guardColumnsOf: %v", err)
	}
	if !hasColumnFold(cols, "updated_at") {
		t.Errorf("hasColumnFold(%v, updated_at) = false, want true", cols)
	}
	spec := updatedAtGuards[repairKey{"ignored_schema_migrations", 7}]
	if got := spec.presentFlag("wisps", cols); got != "is_blocked" {
		t.Errorf("presentFlag = %q, want is_blocked", got)
	}
	if got, want := fingerprintColumns(cols), []string{"ID", "Title", "IS_BLOCKED"}; !slices.Equal(got, want) {
		t.Errorf("fingerprintColumns = %v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
