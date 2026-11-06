package schema

import (
	"context"
	"regexp"
	"testing"
	"testing/fstest"

	"github.com/DATA-DOG/go-sqlmock"
)

// These tests pin the re-arm half of gastownhall/beads#7037's AC-7: the DELETE
// that makes ignored 0032 pending again must take the whole cursor suffix from
// 32 up (a MAX(version) cursor hides the repair behind any higher row), and
// the replay that follows must run every step it uncovered in order —
// including ignored migrations that do not exist yet, which only a synthetic
// suffix can prove.

// TestRearmIgnoredIsBlockedRepairDeletesCursorSuffixAtAndAbove32 pins the
// DELETE's shape: one statement, scoped to versions >= 32, fired only when the
// clone-local cursor table exists.
func TestRearmIgnoredIsBlockedRepairDeletesCursorSuffixAtAndAbove32(t *testing.T) {
	t.Run("table exists deletes the suffix", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		expectShowTable(mock, "ignored_schema_migrations", true)
		mock.ExpectExec(regexp.QuoteMeta(
			"DELETE FROM ignored_schema_migrations WHERE version >= ?")).
			WithArgs(isBlockedRepairIgnoredVersion).
			WillReturnResult(sqlmock.NewResult(0, 1))

		if err := rearmIgnoredIsBlockedRepair(context.Background(), db); err != nil {
			t.Fatalf("rearmIgnoredIsBlockedRepair: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("missing cursor table is a no-op", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()

		// No ExpectExec: a DELETE after the missing-table probe would surface
		// here as an unexpected call.
		expectShowTable(mock, "ignored_schema_migrations", false)

		if err := rearmIgnoredIsBlockedRepair(context.Background(), db); err != nil {
			t.Fatalf("rearmIgnoredIsBlockedRepair: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}

// TestRearmSuffixReplaysInOrderThroughLaterIgnoredMigrations drives the runner
// with a synthetic ignored-series suffix the real bundle cannot express yet:
// after the re-arm DELETE uncovers 32, every later step (a hypothetical 0033)
// replays too, in version order, each running its registered pre-migration
// repair before its body and recording its cursor row after it. sqlmock
// matches expectations in order, so the expectation sequence is the ordering
// assertion; the repair hooks also record their own firing order as a
// second, independent witness.
func TestRearmSuffixReplaysInOrderThroughLaterIgnoredMigrations(t *testing.T) {
	quietStderr(t)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	fsys := fstest.MapFS{
		"syn/0032_recompute_mixed_is_blocked.up.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		"syn/0033_future_marker.up.sql":              &fstest.MapFile{Data: []byte("UPDATE syn_probe SET n = n + 1;")},
	}
	src := migrationSource{files: fsys, dir: "syn", cursorTable: "syn_schema_migrations"}

	var fired []int
	for _, version := range []int{32, 33} {
		v := version
		preMigrationRepairs[repairKey{src.cursorTable, v}] = func(ctx context.Context, db DBConn) error {
			fired = append(fired, v)
			if _, err := db.ExecContext(ctx, "INSERT INTO syn_writes (step) VALUES (?)", v); err != nil {
				return err
			}
			return nil
		}
	}
	t.Cleanup(func() {
		delete(preMigrationRepairs, repairKey{src.cursorTable, 32})
		delete(preMigrationRepairs, repairKey{src.cursorTable, 33})
	})

	// Step 32: its repair, its frozen body, only then its cursor row.
	mock.ExpectExec("INSERT INTO syn_writes").WithArgs(32).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("SELECT 1;")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT IGNORE INTO syn_schema_migrations").WithArgs(32, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Step 33, the later migration the DELETE uncovered with 32: same shape.
	mock.ExpectExec("INSERT INTO syn_writes").WithArgs(33).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE syn_probe SET n = n + 1;")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT IGNORE INTO syn_schema_migrations").WithArgs(33, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	applied, err := runMigrations(context.Background(), db, src, 31, 0, false)
	if err != nil {
		t.Fatalf("runMigrations over the re-armed suffix: %v", err)
	}
	if applied != 2 {
		t.Fatalf("applied = %d, want 2 (both the repair marker and the later step)", applied)
	}
	if len(fired) != 2 || fired[0] != 32 || fired[1] != 33 {
		t.Errorf("repair hooks fired %v, want [32 33] — the suffix replays in version order", fired)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations (ordering mismatch would surface here): %v", err)
	}
}
