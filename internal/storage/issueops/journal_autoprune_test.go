package issueops

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// The auto-prune target is "everything the floors do not protect". These pin
// that it is computed from head+1 through the SAME resolver an explicit prune
// uses, and that the one case where automatic and manual pruning must differ —
// both floors disabled — is refused here rather than delegated.

func TestComputeEventsAutoPruneBoundIsANoOpWithBothFloorsDisabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// No expectations at all: with nothing to protect the journal, maintenance
	// must not so much as read the counter. `bd events prune --before <head+1>`
	// with the same floors would delete everything, which is exactly the
	// difference — one was asked for.
	bound, skip, err := ComputeEventsAutoPruneBoundInTx(context.Background(), db, 0, 0, time.Now())
	if err != nil {
		t.Fatalf("compute bound: %v", err)
	}
	if !skip || bound != 0 {
		t.Fatalf("bound=%d skip=%v, want skip with both floors disabled — an unbounded ledger is a choice, not a backlog", bound, skip)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestComputeEventsAutoPruneBoundStopsAtTheNarrowestFloor(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		head       int64
		retainDays int
		retainRows int
		rowsCeil   *int64
		daysFloor  *int64
		wantBound  int64
		wantSkip   bool
	}{
		{
			name:       "rows floor alone bounds an unbounded request",
			head:       100,
			retainRows: 10,
			rowsCeil:   ptr(int64(90)),
			wantBound:  91,
		},
		{
			name:       "days floor alone bounds an unbounded request",
			head:       100,
			retainDays: 7,
			daysFloor:  ptr(int64(60)),
			wantBound:  60,
		},
		{
			name:       "both floors: the narrower one wins",
			head:       100,
			retainDays: 7,
			retainRows: 10,
			rowsCeil:   ptr(int64(90)),
			daysFloor:  ptr(int64(60)),
			wantBound:  60,
		},
		{
			name:       "both floors: the narrower one wins the other way",
			head:       100,
			retainDays: 7,
			retainRows: 50,
			rowsCeil:   ptr(int64(50)),
			daysFloor:  ptr(int64(95)),
			wantBound:  51,
		},
		{
			name:       "fewer rows than the floor retains protects everything",
			head:       5,
			retainRows: 10,
			rowsCeil:   nil,
			wantSkip:   true,
		},
		{
			name:       "the whole journal is inside the age window",
			head:       100,
			retainDays: 7,
			daysFloor:  ptr(int64(1)),
			wantSkip:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectQuery(regexp.QuoteMeta("SELECT next_seq FROM bd_events_seq")).
				WillReturnRows(sqlmock.NewRows([]string{"next_seq"}).AddRow(tc.head))
			if tc.retainRows > 0 {
				rows := sqlmock.NewRows([]string{"seq"})
				if tc.rowsCeil != nil {
					rows.AddRow(*tc.rowsCeil)
				}
				mock.ExpectQuery(regexp.QuoteMeta(EventsPruneRowsCeilQuery())).
					WithArgs(tc.retainRows).WillReturnRows(rows)
			}
			if tc.retainDays > 0 {
				// A nil floor is the NULL row MIN() returns when no row is
				// young enough — "the age floor constrains nothing".
				var floor any
				if tc.daysFloor != nil {
					floor = *tc.daysFloor
				}
				mock.ExpectQuery(regexp.QuoteMeta(EventsPruneDaysFloorQuery())).
					WillReturnRows(sqlmock.NewRows([]string{"floor"}).AddRow(floor))
			}

			bound, skip, err := ComputeEventsAutoPruneBoundInTx(context.Background(), db, tc.retainDays, tc.retainRows, now)
			if err != nil {
				t.Fatalf("compute bound: %v", err)
			}
			if skip != tc.wantSkip {
				t.Fatalf("skip = %v, want %v (bound %d)", skip, tc.wantSkip, bound)
			}
			if !tc.wantSkip && bound != tc.wantBound {
				t.Errorf("bound = %d, want %d", bound, tc.wantBound)
			}
		})
	}
}

// TestComputeEventsAutoPruneBoundSkipsAnEmptyJournal: head 0 means nothing has
// ever been journaled here. The floors would happily resolve against an empty
// table, but there is nothing to delete and no reason to open a delete
// transaction on every command in a workspace that just enabled the feature.
func TestComputeEventsAutoPruneBoundSkipsAnEmptyJournal(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT next_seq FROM bd_events_seq")).
		WillReturnRows(sqlmock.NewRows([]string{"next_seq"}).AddRow(0))

	_, skip, err := ComputeEventsAutoPruneBoundInTx(context.Background(), db, 7, 100, time.Now())
	if err != nil {
		t.Fatalf("compute bound: %v", err)
	}
	if !skip {
		t.Fatal("an empty journal must skip rather than resolve a bound")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestPruneEventsBatchIsAnOrderedPrefixDelete pins the statement text, because
// the ordering is not decoration: without ORDER BY, a LIMITed delete may remove
// any matching subset, and a hole above the floor is silent record loss that
// the left-edge truncation check cannot report.
func TestPruneEventsBatchIsAnOrderedPrefixDelete(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM bd_events_journal WHERE seq < ? ORDER BY seq ASC LIMIT 500")).
		WithArgs(int64(42)).WillReturnResult(sqlmock.NewResult(0, 7))

	n, err := PruneEventsBatchInTx(context.Background(), db, 42, 500)
	if err != nil {
		t.Fatalf("batch prune: %v", err)
	}
	if n != 7 {
		t.Errorf("deleted = %d, want 7", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestPruneEventsBatchRefusesANonPositiveLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	if _, err := PruneEventsBatchInTx(context.Background(), db, 42, 0); err == nil {
		t.Fatal("a zero batch limit must be refused, not executed as an unbounded delete")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
