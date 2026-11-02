package schema

import (
	"bytes"
	"context"
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
