package schema

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const (
	guardColumnQuery = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS.*TABLE_NAME = \? AND COLUMN_NAME = \?`
	guardTableQuery  = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES.*TABLE_NAME = \?`
	guardIndexQuery  = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.STATISTICS.*TABLE_NAME = \? AND INDEX_NAME = \?`
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

	// 1. Snapshot.
	mock.ExpectQuery(guardColumnQuery).WithArgs("wisps", "updated_at").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(`SELECT id, updated_at FROM wisps ORDER BY id`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).
			AddRow("w-1", "2025-04-02 09:30:32").
			AddRow("w-2", "2025-04-03 09:30:33"))
	// 2. ensureWispIsBlockedForRecompute: everything already in place.
	mock.ExpectQuery(guardTableQuery).WithArgs("wisps").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(guardColumnQuery).WithArgs("wisps", "is_blocked").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(guardIndexQuery).WithArgs("wisps", "idx_wisps_is_blocked").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	// 3. The frozen body.
	mock.ExpectExec(`UPDATE wisps SET is_blocked = 0`).WillReturnResult(sqlmock.NewResult(0, 1))
	// 4. Restore: only w-1 was stamped; w-2 is unchanged and not rewritten.
	mock.ExpectQuery(guardColumnQuery).WithArgs("wisps", "updated_at").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(`SELECT id, updated_at FROM wisps ORDER BY id`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).
			AddRow("w-1", "2026-11-02 04:00:00").
			AddRow("w-2", "2025-04-03 09:30:33"))
	mock.ExpectExec(`UPDATE wisps SET updated_at = CASE id WHEN \? THEN \? ELSE updated_at END WHERE \(id = \? AND updated_at = \?\)$`).
		WithArgs("w-1", "2025-04-02 09:30:32", "w-1", "2026-11-02 04:00:00").
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
			name: "snapshot read fails",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(guardColumnQuery).WithArgs("issues", "updated_at").
					WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
				mock.ExpectQuery(`SELECT id, updated_at FROM issues ORDER BY id`).WillReturnError(injected)
			},
			want: "snapshotting updated_at before migration 0046_",
		},
		{
			name: "restore write fails",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(guardColumnQuery).WithArgs("issues", "updated_at").
					WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
				mock.ExpectQuery(`SELECT id, updated_at FROM issues ORDER BY id`).
					WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).AddRow("bd-1", "2025-03-01 09:30:00"))
				mock.ExpectExec(`SET @needs_add`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery(guardColumnQuery).WithArgs("issues", "updated_at").
					WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
				mock.ExpectQuery(`SELECT id, updated_at FROM issues ORDER BY id`).
					WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).AddRow("bd-1", "2026-11-02 04:00:00"))
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
