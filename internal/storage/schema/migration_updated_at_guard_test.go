package schema

import (
	"context"
	"errors"
	"strings"
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

// updatedGuardWispsSelectRegex is updatedGuardSelectRegex's wisps-table
// counterpart, and updatedGuardWispsRestoreRegex the two-row restore shape
// the 0035 subtests pin.
const (
	updatedGuardWispsSelectRegex = "SELECT id, updated_at FROM `wisps` ORDER BY id"
)
const updatedGuardWispsRestoreRegex = `UPDATE ` + "`wisps`" + ` SET updated_at = CASE id WHEN \? THEN \? WHEN \? THEN \? ELSE updated_at END WHERE \(id = \? AND updated_at = \?\) OR \(id = \? AND updated_at = \?\)`

// TestUpdatedAtGuardRowMovesPreferDestinationHistory pins the 0035/0053
// lookup rules at statement level: a moved row is restored from its
// source-table snapshot, a row that already existed in the restore table
// keeps that table's own value even when the source table holds a same-id
// snapshot, and rows the step created (no snapshot anywhere) or deleted
// (absent from the reread) are left entirely alone.
func TestUpdatedAtGuardRowMovesPreferDestinationHistory(t *testing.T) {
	failOnSwallowedAdvisory(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Snapshot: issues holds the moving row plus a same-id value that must
	// NOT win; wisps holds the destination's own value for that id plus a
	// row the step will delete.
	mock.ExpectQuery(updatedGuardTableQuery).WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(updatedGuardSelectRegex).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).
			AddRow("mover", "2026-01-06 10:00:00").
			AddRow("native", "2026-01-01 00:00:00"))
	mock.ExpectQuery(updatedGuardTableQuery).WithArgs("wisps").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(updatedGuardWispsSelectRegex).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).
			AddRow("doomed", "2026-01-02 00:00:00").
			AddRow("native", "2026-02-10 08:00:00"))
	// 0035's body (bounded pass: no per-step commit).
	mock.ExpectExec(`(?s)SET @shared_cols`).WillReturnResult(sqlmock.NewResult(0, 0))
	// Reread: mover arrived stamped, native (collision) got stamped, fresh
	// was created by the step, doomed is gone. Only mover and native are
	// restored — mover from issues, native from wisps' own value, not the
	// issues value under the same id.
	mock.ExpectQuery(updatedGuardTableQuery).WithArgs("wisps").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(updatedGuardWispsSelectRegex).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).
			AddRow("fresh", updatedGuardNewTS).
			AddRow("mover", updatedGuardNewTS).
			AddRow("native", updatedGuardNewTS))
	mock.ExpectExec(updatedGuardWispsRestoreRegex).
		WithArgs("mover", "2026-01-06 10:00:00", "native", "2026-02-10 08:00:00",
			"mover", updatedGuardNewTS, "native", updatedGuardNewTS).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(35, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if _, err := runMigrations(context.Background(), db, mainSource, 34, 35, false); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations (lookup precedence or skip rules wrong): %v", err)
	}
}

// TestUpdatedAtGuardIssuesNoQueriesForUnregisteredStep pins the guard's
// default-off contract (#5981): a version outside the registry runs with
// zero snapshot, reread, or restore queries — the guard must never become a
// tax on the ordinary migration path. The mock primes ONLY the step's own
// statements (0052's body and its cursor row; 0052 carries no
// pre-migration repair), so a single guard probe or read against a
// mistakenly-registered version surfaces as an unexpected call and fails
// the step. Vacuous against the pre-guard base by construction (there is
// nothing to issue the queries); it exists to pin the registry's shape.
func TestUpdatedAtGuardIssuesNoQueriesForUnregisteredStep(t *testing.T) {
	failOnSwallowedAdvisory(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`(?s)SET @has_old`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT IGNORE INTO schema_migrations`).WithArgs(52, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	applied, err := runMigrations(context.Background(), db, mainSource, 51, 52, false)
	if err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if applied != 1 {
		t.Fatalf("runMigrations applied = %d, want 1", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
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
		mock.ExpectExec(`UPDATE `+"`issues`"+` SET updated_at = CASE id WHEN \? THEN \? WHEN \? THEN \? ELSE updated_at END WHERE \(id = \? AND updated_at = \?\) OR \(id = \? AND updated_at = \?\)`).
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

// TestUpdatedAtGuardSnapshotFailureAbortsStep proves a snapshot failure fails
// the step before anything it could corrupt: no migration body, no cursor row,
// no commit.
func TestUpdatedAtGuardSnapshotFailureAbortsStep(t *testing.T) {
	failOnSwallowedAdvisory(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// commitEachStep primes the dirty-table snapshot first; the failure point
	// is the guard's snapshot read, after its table probe.
	mock.ExpectQuery(updatedGuardDirtyStatusQuery).
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}))
	mock.ExpectQuery(updatedGuardTableQuery).WithArgs("issues").
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(updatedGuardSelectRegex).
		WillReturnError(errors.New("snapshot read failed"))

	applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, true)
	if err == nil || !strings.Contains(err.Error(), "updated_at") {
		t.Fatalf("runMigrations err = %v, want an updated_at snapshot failure", err)
	}
	if applied != 0 {
		t.Fatalf("runMigrations applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations (a body/cursor statement ran after the failure?): %v", err)
	}
}

// TestUpdatedAtGuardRestoreFailureAbortsStep proves a restore failure fails
// the step after a successful body but before the cursor row records: the
// version stays pending, so the next pass re-runs body and restore together.
func TestUpdatedAtGuardRestoreFailureAbortsStep(t *testing.T) {
	failOnSwallowedAdvisory(t)

	t.Run("post-body reread fails", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		updatedGuardPrimeThroughBody(t, mock, true)
		mock.ExpectQuery(updatedGuardSelectRegex).
			WillReturnError(errors.New("post-body read failed"))

		applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, true)
		if err == nil || !strings.Contains(err.Error(), "updated_at") {
			t.Fatalf("runMigrations err = %v, want an updated_at restore failure", err)
		}
		if applied != 0 {
			t.Fatalf("runMigrations applied = %d, want 0 (cursor must not record the step)", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations (a cursor/commit statement ran after the failure?): %v", err)
		}
	})

	t.Run("restore statement fails", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		updatedGuardPrimeThroughBody(t, mock, true)
		mock.ExpectQuery(updatedGuardSelectRegex).
			WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at"}).AddRow("i1", updatedGuardNewTS))
		mock.ExpectExec(updatedGuardRestoreRegex).
			WithArgs("i1", updatedGuardOldTS, "i1", updatedGuardNewTS).
			WillReturnError(errors.New("restore write failed"))

		applied, err := runMigrations(context.Background(), db, mainSource, 45, 46, true)
		if err == nil || !strings.Contains(err.Error(), "updated_at") {
			t.Fatalf("runMigrations err = %v, want an updated_at restore failure", err)
		}
		if applied != 0 {
			t.Fatalf("runMigrations applied = %d, want 0 (cursor must not record the step)", applied)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations (a cursor/commit statement ran after the failure?): %v", err)
		}
	})
}
