package schema

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// updated_at guard tests (#5981, bd-chqckf). The guard snapshots
// (id, updated_at) before a registered value-rewriting migration step and
// restores the historical timestamps after its body, before the cursor row
// and per-step commit record the step. These sqlmock tests pin the runner's
// statement ORDER and the restore statement's shape; the real-Dolt outcomes
// are pinned in internal/storage/embeddeddolt
// (migrate_updated_at_preservation_test.go) and internal/storage/dolt
// (migration_is_blocked_updated_at_test.go).

const (
	// updatedGuardOldTS is a row's historical timestamp, the value that must
	// survive the step.
	updatedGuardOldTS = "2026-01-15 10:00:00"
	// updatedGuardNewTS is what the migration body stamps over it.
	updatedGuardNewTS = "2026-11-02 05:00:00"
)

// updatedGuardSelectRegex matches the guard's snapshot/reread query.
const updatedGuardSelectRegex = "SELECT id, updated_at FROM `issues` ORDER BY id"

// updatedGuardRestoreRegex matches the single-row shape of the guard's
// compare-and-swap restore statement.
const updatedGuardRestoreRegex = "UPDATE `issues` SET updated_at = CASE id WHEN \\? THEN \\? ELSE updated_at END WHERE \\(id = \\? AND updated_at = \\?\\)"

// updatedGuardTableQuery matches the guard's table-existence probe.
const updatedGuardTableQuery = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES.*TABLE_NAME = \?`

// updatedGuardDirtyStatusQuery matches the runner's dolt_status snapshot.
const updatedGuardDirtyStatusQuery = `(?s)SELECT s\.table_name, s\.staged\s+FROM dolt_status s\s+WHERE NOT EXISTS`

// updatedGuardPrimeThroughBody primes everything runMigrations issues for
// migration 0046 (a registered step) from v45 up to and including the body:
// the pre-step dirty-table snapshot (commitEachStep path only), the guard's
// existence probe and snapshot read, the frozen body, and the restore path's
// existence probe. The caller primes the post-body reread (rows or error),
// any restore statement, and the tail via updatedGuardPrimeTail, so each test
// shapes its own failure point.
func updatedGuardPrimeThroughBody(t *testing.T, mock sqlmock.Sqlmock, commitEachStep bool) {
	t.Helper()
	if commitEachStep {
		mock.ExpectQuery(updatedGuardDirtyStatusQuery).
			WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}))
	}
	mock.ExpectQuery(updatedGuardTableQuery).WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(updatedGuardSelectRegex).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).AddRow("i1", updatedGuardOldTS))
	// No pre-migration repair is registered at version 46.
	mock.ExpectExec(`(?s)SET @needs_add`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(updatedGuardTableQuery).WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
}

// updatedGuardPrimeTail primes the cursor-row insert and, on the
// commitEachStep path, the step's atomic commit of the dirtied tables.
func updatedGuardPrimeTail(mock sqlmock.Sqlmock, commitEachStep bool) {
	mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(46, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if commitEachStep {
		mock.ExpectQuery(updatedGuardDirtyStatusQuery).
			WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}).AddRow("issues", false))
		mock.ExpectQuery(`CALL DOLT_ADD\('-f', \?\)`).WithArgs("issues").
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
		mock.ExpectQuery(`CALL DOLT_ADD\('-f', \?\)`).WithArgs("schema_migrations").
			WillReturnRows(sqlmock.NewRows([]string{"status"}))
		mock.ExpectQuery(`CALL DOLT_COMMIT`).
			WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	}
}

// TestUpdatedAtGuardRestoresTimestampsAroundRegisteredStep pins the guard's
// position in the step: snapshot before the body, restore between a
// successful body and the cursor row, so the restored values land in the same
// atomic per-step commit as the version that produced them. On the bounded
// (MigrateUpTo) path there is no per-step commit, but the restore and its
// ordering around the cursor row are identical.
func TestUpdatedAtGuardRestoresTimestampsAroundRegisteredStep(t *testing.T) {
	failOnSwallowedAdvisory(t)

	postBodyStamped := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "updated_at"}).AddRow("i1", updatedGuardNewTS)
	}

	t.Run("restore precedes cursor row and step commit", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		updatedGuardPrimeThroughBody(t, mock, true)
		// The body stamped i1, so the post-body reread reports the
		// migration-time value and the guard must compare-and-swap it back
		// BEFORE the cursor row records.
		mock.ExpectQuery(updatedGuardSelectRegex).WillReturnRows(postBodyStamped())
		mock.ExpectExec(updatedGuardRestoreRegex).
			WithArgs("i1", updatedGuardOldTS, "i1", updatedGuardNewTS).
			WillReturnResult(sqlmock.NewResult(0, 1))
		updatedGuardPrimeTail(mock, true)

		applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, true)
		if err != nil {
			t.Fatalf("runMigrations: %v", err)
		}
		if applied != 1 {
			t.Fatalf("runMigrations applied = %d, want 1", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations (statement order would surface here): %v", err)
		}
	})

	t.Run("bounded pass restores the same way", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		updatedGuardPrimeThroughBody(t, mock, false)
		mock.ExpectQuery(updatedGuardSelectRegex).WillReturnRows(postBodyStamped())
		mock.ExpectExec(updatedGuardRestoreRegex).
			WithArgs("i1", updatedGuardOldTS, "i1", updatedGuardNewTS).
			WillReturnResult(sqlmock.NewResult(0, 1))
		updatedGuardPrimeTail(mock, false)

		applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, false)
		if err != nil {
			t.Fatalf("runMigrations: %v", err)
		}
		if applied != 1 {
			t.Fatalf("runMigrations applied = %d, want 1", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("rows the body left alone are not rewritten", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		updatedGuardPrimeThroughBody(t, mock, false)
		// Post-body value equals the snapshot: no restore statement is primed
		// at all, so a single issued UPDATE would surface as an unexpected
		// call and an unmet-expectations failure.
		mock.ExpectQuery(updatedGuardSelectRegex).
			WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).AddRow("i1", updatedGuardOldTS))
		updatedGuardPrimeTail(mock, false)

		if _, err := runMigrations(context.Background(), db, mainSource, 45, 46, false); err != nil {
			t.Fatalf("runMigrations: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("a compare-and-swap miss is not an error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		updatedGuardPrimeThroughBody(t, mock, false)
		mock.ExpectQuery(updatedGuardSelectRegex).WillReturnRows(postBodyStamped())
		// Another writer changed the row between the reread and the restore:
		// the predicate matches nothing (0 rows affected). The guard's CAS
		// means that is a deliberate leave-alone, not a failure — the step
		// still records, exactly as it would for any concurrent write the
		// migration does not own.
		mock.ExpectExec(updatedGuardRestoreRegex).
			WithArgs("i1", updatedGuardOldTS, "i1", updatedGuardNewTS).
			WillReturnResult(sqlmock.NewResult(0, 0))
		updatedGuardPrimeTail(mock, false)

		applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, false)
		if err != nil {
			t.Fatalf("runMigrations: %v (a CAS miss must not fail the step)", err)
		}
		if applied != 1 {
			t.Fatalf("runMigrations applied = %d, want 1", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	})

	t.Run("rows past the batch boundary split into bounded statements", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		prevBatch := updatedAtRestoreBatchRows
		updatedAtRestoreBatchRows = 2
		t.Cleanup(func() { updatedAtRestoreBatchRows = prevBatch })

		mock.ExpectQuery(updatedGuardTableQuery).WithArgs("issues").
			WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
		mock.ExpectQuery(updatedGuardSelectRegex).
			WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).
				AddRow("i1", "2026-01-10 09:00:00").
				AddRow("i2", "2026-01-11 09:00:00").
				AddRow("i3", "2026-01-12 09:00:00"))
		mock.ExpectExec(`(?s)SET @needs_add`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(updatedGuardTableQuery).WithArgs("issues").
			WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
		mock.ExpectQuery(updatedGuardSelectRegex).
			WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).
				AddRow("i1", updatedGuardNewTS).
				AddRow("i2", updatedGuardNewTS).
				AddRow("i3", updatedGuardNewTS))
		// i1 and i2 fill one statement (two WHEN arms, two OR-ed predicates);
		// i3 spills into a second statement in the single-row shape.
		mock.ExpectExec(`UPDATE ` + "`issues`" + ` SET updated_at = CASE id WHEN \? THEN \? WHEN \? THEN \? ELSE updated_at END WHERE \(id = \? AND updated_at = \?\) OR \(id = \? AND updated_at = \?\)`).
			WithArgs("i1", "2026-01-10 09:00:00", "i2", "2026-01-11 09:00:00",
				"i1", updatedGuardNewTS, "i2", updatedGuardNewTS).
			WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(updatedGuardRestoreRegex).
			WithArgs("i3", "2026-01-12 09:00:00", "i3", updatedGuardNewTS).
			WillReturnResult(sqlmock.NewResult(0, 1))
		updatedGuardPrimeTail(mock, false)

		if _, err := runMigrations(context.Background(), db, mainSource, 45, 46, false); err != nil {
			t.Fatalf("runMigrations: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations (batch shape or order wrong): %v", err)
		}
	})
}
