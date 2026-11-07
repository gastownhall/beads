package schema

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// The tests here drive restoreWispIsBlocked itself over sqlmock, pinning its
// probe count and its write order statement by statement. They reference the
// repair directly, so they exist only from the fix on; the marker-level pins
// that compile against the shipped tree live in wisp_is_blocked_restore_test.go.

const (
	wispTableProbe  = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES.*TABLE_NAME = \?`
	wispColumnProbe = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS.*COLUMN_NAME = \?`
	wispIndexProbe  = `(?s)SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.STATISTICS.*INDEX_NAME = \?`
)

// expectWispShapeProbe queues one INFORMATION_SCHEMA existence probe answering
// with count.
func expectWispShapeProbe(mock sqlmock.Sqlmock, pattern string, args []driver.Value, count int) {
	mock.ExpectQuery(pattern).WithArgs(args...).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(count))
}

// TestIgnored32HealthyClonePaysThreeProbes pins the steady-state cost of the
// 0032 repair on the fleet's healthy clones: exactly the three existence
// probes and not one write. The expectations below are exhaustive -- an
// unexpected statement fails the run through sqlmock itself.
func TestIgnored32HealthyClonePaysThreeProbes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	expectWispShapeProbe(mock, wispTableProbe, []driver.Value{"wisps"}, 1)
	expectWispShapeProbe(mock, wispColumnProbe, []driver.Value{"wisps", "is_blocked"}, 1)
	expectWispShapeProbe(mock, wispIndexProbe, []driver.Value{"wisps", "idx_wisps_is_blocked"}, 1)

	if err := restoreWispIsBlocked(context.Background(), db); err != nil {
		t.Fatalf("restoreWispIsBlocked on a healthy clone: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("healthy clone must pay exactly the three probes: %v", err)
	}
}

// TestIgnored32AbsentWispsTableIsANoOp pins that a clone with no wisps table
// yet pays a single probe and the repair returns nil: ignored/0001's guarded
// create is what materializes wisps further down the pass, and the restore
// must not second-guess it.
func TestIgnored32AbsentWispsTableIsANoOp(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	expectWispShapeProbe(mock, wispTableProbe, []driver.Value{"wisps"}, 0)

	if err := restoreWispIsBlocked(context.Background(), db); err != nil {
		t.Fatalf("restoreWispIsBlocked without wisps: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the absent-wisps path must stop at its first probe: %v", err)
	}
}

// TestIgnored32DropsStaleIndexBeforeRestore pins the drifted shape's write
// order: a surviving idx_wisps_is_blocked over the dropped column's table
// goes first, then the column returns with ignored/0006's definition, and
// the index is re-created only after both, over (is_blocked, status).
func TestIgnored32DropsStaleIndexBeforeRestore(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	expectWispShapeProbe(mock, wispTableProbe, []driver.Value{"wisps"}, 1)
	expectWispShapeProbe(mock, wispColumnProbe, []driver.Value{"wisps", "is_blocked"}, 0)
	expectWispShapeProbe(mock, wispIndexProbe, []driver.Value{"wisps", "idx_wisps_is_blocked"}, 1)
	mock.ExpectExec("DROP INDEX idx_wisps_is_blocked ON wisps").
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectWispShapeProbe(mock, wispColumnProbe, []driver.Value{"wisps", "is_blocked"}, 0)
	mock.ExpectExec("ALTER TABLE wisps ADD COLUMN is_blocked TINYINT\\(1\\) NOT NULL DEFAULT 0").
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectWispShapeProbe(mock, wispIndexProbe, []driver.Value{"wisps", "idx_wisps_is_blocked"}, 0)
	mock.ExpectExec("CREATE INDEX idx_wisps_is_blocked ON wisps\\(is_blocked, status\\)").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := restoreWispIsBlocked(context.Background(), db); err != nil {
		t.Fatalf("restoreWispIsBlocked on a column-drifted clone: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the restore must drop the stale index, add the column, then re-create the index last: %v", err)
	}
}
