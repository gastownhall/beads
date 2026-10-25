//go:build integration && !windows

package dolt

import (
	"context"
	"testing"
)

// TestMigration0069WidensPrecisionZeroStoreAsRawSQL pins migration 0069 on
// the path existing stores actually take. Every clone that applied 0068
// before 0069 existed has change_at and removed_at at precision 0, and it
// reaches DATETIME(6) only through 0069's DATETIME_PRECISION-guarded PREPARE
// blocks, run as raw SQL -- never through the CLI-bundle override, which is
// all TestMigration0069ChangeAtSurvivesSubSecondPrecisionThroughDoltCLI in
// internal/storage/schema exercises. Without this test, deleting either
// guarded block from the .up.sql fails only the byte pins in that package.
//
// setupTestStore migrates to latest, so the raw down (itself under test
// here) is what puts the store back at precision 0. Then the raw up runs
// twice through runMigrationSQL, the same executor the pr4107 replay harness
// uses: pass 1 must fire both guards, and pass 2 must be a clean no-op on
// the already-widened store. A whole-second row written at precision 0 has
// to survive the widen unchanged -- the up file's "widening is lossless"
// claim -- and afterwards a sub-second write has to round-trip intact.
func TestMigration0069WidensPrecisionZeroStoreAsRawSQL(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	const upFile = "../schema/migrations/0069_widen_issue_versions_datetime_precision.up.sql"
	const downFile = "../schema/migrations/0069_widen_issue_versions_datetime_precision.down.sql"

	requireColumnType(ctx, t, store, "issue_versions", "change_at", "datetime(6)")
	requireColumnType(ctx, t, store, "issue_versions", "removed_at", "datetime(6)")

	runMigrationSQL(t, ctx, store, downFile)
	requireColumnType(ctx, t, store, "issue_versions", "change_at", "datetime")
	requireColumnType(ctx, t, store, "issue_versions", "removed_at", "datetime")

	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO issue_versions (issue_id, revision, epoch, change_at, attribution_status)
		VALUES ('mig0069-whole-second', 1, 1, '2026-09-05 00:00:07', 'unknown')`); err != nil {
		t.Fatalf("seed a precision-0 issue_versions row: %v", err)
	}

	for pass := 1; pass <= 2; pass++ {
		runMigrationSQL(t, ctx, store, upFile)
		requireColumnType(ctx, t, store, "issue_versions", "change_at", "datetime(6)")
		requireColumnType(ctx, t, store, "issue_versions", "removed_at", "datetime(6)")
		if got, want := readChangeAt(ctx, t, store, "mig0069-whole-second"), "2026-09-05 00:00:07.000000"; got != want {
			t.Fatalf("pass %d: whole-second change_at = %q after the widen, want %q unchanged", pass, got, want)
		}
	}

	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO issue_versions (issue_id, revision, epoch, change_at, attribution_status)
		VALUES ('mig0069-sub-second', 1, 1, '2026-09-01 00:00:00.750000', 'unknown')`); err != nil {
		t.Fatalf("insert a sub-second issue_versions row: %v", err)
	}
	if got, want := readChangeAt(ctx, t, store, "mig0069-sub-second"), "2026-09-01 00:00:00.750000"; got != want {
		t.Fatalf("sub-second change_at = %q, want %q (at precision 0 Dolt rounds .750000 up to the next second)", got, want)
	}
}

// requireColumnType fails unless INFORMATION_SCHEMA reports table.column with
// exactly this COLUMN_TYPE, which is where a DATETIME's precision shows:
// "datetime" at precision 0, "datetime(6)" at microseconds.
func requireColumnType(ctx context.Context, t *testing.T, store *DoltStore, table, column, want string) {
	t.Helper()
	var got string
	if err := store.db.QueryRowContext(ctx, `
		SELECT COLUMN_TYPE FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		table, column).Scan(&got); err != nil {
		t.Fatalf("read %s.%s column type: %v", table, column, err)
	}
	if got != want {
		t.Fatalf("%s.%s column type = %q, want %q", table, column, got, want)
	}
}

// readChangeAt renders one row's change_at with DATE_FORMAT rather than
// scanning the driver's value, so the comparison sees every stored
// microsecond whatever the DSN's parseTime setting is.
func readChangeAt(ctx context.Context, t *testing.T, store *DoltStore, issueID string) string {
	t.Helper()
	var got string
	if err := store.db.QueryRowContext(ctx,
		"SELECT DATE_FORMAT(change_at, '%Y-%m-%d %H:%i:%s.%f') FROM issue_versions WHERE issue_id = ?",
		issueID).Scan(&got); err != nil {
		t.Fatalf("read change_at for %s: %v", issueID, err)
	}
	return got
}
