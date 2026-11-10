package schema

import (
	"context"
	"errors"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// TestCallReturningRowCapturesFirstRowAndStillDrains is the contract for the
// helper that lets a caller read what a Dolt procedure reported without giving
// up the drain that keeps a pinned connection usable.
//
// The drain half is the load-bearing half — see DrainCall's comment for the
// go-sql-driver error-path asymmetry it exists to survive — so the case feeds
// back TWO result sets with several rows each. Capturing must not shorten the
// walk: if it did, the surplus rows and the whole second result set would be
// left queued on the wire, which is exactly the "busy buffer" ->
// "driver: bad connection" bug DrainCall was written to prevent.
func TestCallReturningRowCapturesFirstRowAndStillDrains(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	first := sqlmock.NewRows([]string{"fast_forward", "conflicts", "message"}).
		AddRow(int64(0), int64(0), "merge successful").
		AddRow(int64(1), int64(1), "a later row that must be drained, not captured")
	second := sqlmock.NewRows([]string{"other"}).
		AddRow("a whole second result set that must be drained")

	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_PULL(?, ?)")).
		WithArgs("origin", "main").
		WillReturnRows(first, second)

	row, err := CallReturningRow(context.Background(), db, "CALL DOLT_PULL(?, ?)", "origin", "main")
	if err != nil {
		t.Fatalf("CallReturningRow: %v", err)
	}

	if msg, ok := row.Str("message"); !ok || msg != "merge successful" {
		t.Errorf(`row.Str("message") = %q, %v; want "merge successful", true`, msg, ok)
	}
	if ff, ok := row.Int("fast_forward"); !ok || ff != 0 {
		t.Errorf(`row.Int("fast_forward") = %d, %v; want 0, true`, ff, ok)
	}
	if c, ok := row.Int("conflicts"); !ok || c != 0 {
		t.Errorf(`row.Int("conflicts") = %d, %v; want 0, true`, c, ok)
	}

	// The capture is the FIRST row, not the last one seen while draining.
	if msg, _ := row.Str("message"); msg != "merge successful" {
		t.Errorf("captured row was overwritten while draining: message = %q", msg)
	}
	// Nothing from the second result set leaked into the captured row.
	if _, ok := row.Str("other"); ok {
		t.Error(`captured row contains "other" from the second result set`)
	}

	// sqlmock reports an unconsumed result set as an unmet expectation, so
	// this is the assertion that the drain really walked to the end.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("statement was not fully drained: %v", err)
	}
}

// TestCallReturningRowEdgeCases covers the shapes a Dolt procedure row actually
// takes: a NULL message (DOLT_PULL returns NULL whenever its internal message
// is empty), a procedure that returns no rows at all, and a failing query.
func TestCallReturningRowEdgeCases(t *testing.T) {
	t.Run("null column reports absent, not empty", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectQuery("CALL DOLT_PULL").WillReturnRows(
			sqlmock.NewRows([]string{"fast_forward", "conflicts", "message"}).
				AddRow(int64(0), int64(0), nil))

		row, err := CallReturningRow(context.Background(), db, "CALL DOLT_PULL()")
		if err != nil {
			t.Fatalf("CallReturningRow: %v", err)
		}
		if v, ok := row.Str("message"); ok {
			t.Errorf(`NULL message reported present with value %q`, v)
		}
		if _, ok := row.Str("no_such_column"); ok {
			t.Error("absent column reported present")
		}
		if _, ok := row.Int("message"); ok {
			t.Error("NULL message parsed as an integer")
		}
	})

	t.Run("no rows yields a nil row and no error", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectQuery("CALL DOLT_ADD").WillReturnRows(sqlmock.NewRows([]string{"status"}))

		row, err := CallReturningRow(context.Background(), db, "CALL DOLT_ADD('.')")
		if err != nil {
			t.Fatalf("CallReturningRow: %v", err)
		}
		if row != nil {
			t.Errorf("row = %v, want nil", row)
		}
		// Reading from a nil row must report absent, not panic.
		if _, ok := row.Str("status"); ok {
			t.Error("nil row reported a value")
		}
		if _, ok := row.Int("status"); ok {
			t.Error("nil row reported an integer")
		}
	})

	t.Run("query error propagates", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		boom := errors.New("branch not found")
		mock.ExpectQuery("CALL DOLT_PULL").WillReturnError(boom)

		row, err := CallReturningRow(context.Background(), db, "CALL DOLT_PULL()")
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
		if row != nil {
			t.Errorf("row = %v, want nil on error", row)
		}
	})
}

// TestDrainCallStillDiscardsEverything is the control for the refactor that put
// DrainCall and CallReturningRow on one shared drain loop: DrainCall's own
// behaviour must be unchanged. It walks every result set and reports only the
// statement's error — a row it cannot decode is not DrainCall's problem, and
// must not become a new way for a migration to fail.
func TestDrainCallStillDiscardsEverything(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
		WithArgs("m").
		WillReturnRows(
			sqlmock.NewRows([]string{"hash"}).AddRow("abc").AddRow("def"),
			sqlmock.NewRows([]string{"extra"}).AddRow(nil),
		)

	if err := DrainCall(context.Background(), db, "CALL DOLT_COMMIT('-m', ?)", "m"); err != nil {
		t.Fatalf("DrainCall: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("DrainCall stopped draining early: %v", err)
	}
}
