package schema

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// The tests here drive restoreWispIsBlocked itself over sqlmock, pinning its
// probe count, its write order statement by statement, and the errors its
// probes surface. They reference the repair directly, so they exist only from
// the fix on; the marker-level pins that compile against the shipped tree
// live in wisp_is_blocked_restore_test.go.

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
// goes first, then the column returns with ignored/0006's definition, then
// every wisp's flag is recomputed from the live graph (the column returns
// with DEFAULT 0, which is not a computed answer), and the index is
// re-created only after all of that, over (is_blocked, status).
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
	// The recompute: list every wisp (non-empty, so a pass runs), read both
	// explained-parent sets (empty here -- nothing to prune, no climb), then
	// one mark and one unmark over wisps. RowsAffected 0 on both means a
	// consistent plane: the fixpoint loop exits after the single pass.
	mock.ExpectQuery("SELECT id FROM wisps").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("wh-b").AddRow("wh-c").AddRow("wh-d"))
	mock.ExpectQuery(`(?s)WITH\s+cand\(pid\) AS \(.*JOIN issues pp`).
		WillReturnRows(sqlmock.NewRows([]string{"pid"}))
	mock.ExpectQuery(`(?s)WITH\s+cand\(pid\) AS \(.*JOIN wisps pp`).
		WillReturnRows(sqlmock.NewRows([]string{"pid"}))
	mock.ExpectExec(`UPDATE wisps w SET w\.is_blocked = 1`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE wisps w SET w\.is_blocked = 0`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectWispShapeProbe(mock, wispIndexProbe, []driver.Value{"wisps", "idx_wisps_is_blocked"}, 0)
	mock.ExpectExec("CREATE INDEX idx_wisps_is_blocked ON wisps\\(is_blocked, status\\)").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := restoreWispIsBlocked(context.Background(), db); err != nil {
		t.Fatalf("restoreWispIsBlocked on a column-drifted clone: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("the restore must drop the stale index, add the column, recompute the flags, then re-create the index last: %v", err)
	}
}

// TestIgnored32PropagatesProbeErrors pins that a failing existence probe
// surfaces as the repair's own contextual error rather than being swallowed
// into a wrong branch (a probe error is not a count of zero).
func TestIgnored32PropagatesProbeErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern string
		args    []driver.Value
		wrap    string
	}{
		{"wisps table probe", wispTableProbe, []driver.Value{"wisps"}, "checking wisps table"},
		{"column probe", wispColumnProbe, []driver.Value{"wisps", "is_blocked"}, "checking wisps.is_blocked column"},
		{"index probe", wispIndexProbe, []driver.Value{"wisps", "idx_wisps_is_blocked"}, "checking idx_wisps_is_blocked index"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			// The probes before the failing one succeed: table and column
			// present, so the order reaches the case's own probe.
			switch tc.name {
			case "column probe":
				expectWispShapeProbe(mock, wispTableProbe, []driver.Value{"wisps"}, 1)
			case "index probe":
				expectWispShapeProbe(mock, wispTableProbe, []driver.Value{"wisps"}, 1)
				expectWispShapeProbe(mock, wispColumnProbe, []driver.Value{"wisps", "is_blocked"}, 1)
			}
			mock.ExpectQuery(tc.pattern).WithArgs(tc.args...).
				WillReturnError(errors.New("probe dropped"))

			err = restoreWispIsBlocked(context.Background(), db)
			if err == nil {
				t.Fatal("restoreWispIsBlocked swallowed the probe error")
			}
			if !strings.Contains(err.Error(), tc.wrap) || !strings.Contains(err.Error(), "probe dropped") {
				t.Fatalf("restoreWispIsBlocked err = %v, want %q wrapping the probe error", err, tc.wrap)
			}
		})
	}
}
