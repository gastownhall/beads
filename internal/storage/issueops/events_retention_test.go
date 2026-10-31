package issueops

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// The count and the delete are the same predicate over the same cutoff, bound
// as UTC, so what the dry run reports is what the prune removes.
func TestEventsRetentionCountAndPruneShareTheCutoffPredicate(t *testing.T) {
	ctx := context.Background()
	_, mock, tx := beginMockTx(t)

	local := time.FixedZone("plus9", 9*3600)
	cutoff := time.Date(2026, 10, 23, 9, 0, 0, 0, local) // 2026-10-23T00:00:00Z
	wantArg := cutoff.UTC()

	mock.ExpectQuery(regexp.QuoteMeta(CountEventsBeforeSQL)).
		WithArgs(wantArg).
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(76593))
	n, err := CountEventsBeforeInTx(ctx, tx, cutoff)
	if err != nil {
		t.Fatalf("CountEventsBeforeInTx: %v", err)
	}
	if n != 76593 {
		t.Fatalf("count = %d, want 76593", n)
	}

	mock.ExpectExec(regexp.QuoteMeta(PruneEventsBeforeSQL)).
		WithArgs(wantArg).
		WillReturnResult(sqlmock.NewResult(0, 76593))
	n, err = PruneEventsBeforeInTx(ctx, tx, cutoff)
	if err != nil {
		t.Fatalf("PruneEventsBeforeInTx: %v", err)
	}
	if n != 76593 {
		t.Fatalf("pruned = %d, want 76593", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A failed delete surfaces as an error naming the cutoff, never as zero rows.
func TestEventsRetentionPruneSurfacesTheDriverError(t *testing.T) {
	ctx := context.Background()
	_, mock, tx := beginMockTx(t)
	cutoff := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	boom := errors.New("table locked")
	mock.ExpectExec(regexp.QuoteMeta(PruneEventsBeforeSQL)).WithArgs(cutoff).WillReturnError(boom)
	n, err := PruneEventsBeforeInTx(ctx, tx, cutoff)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapping %v", err, boom)
	}
	if n != 0 {
		t.Fatalf("pruned = %d on error, want 0", n)
	}
	if err == nil || !regexp.MustCompile(`2026-10-23T00:00:00Z`).MatchString(err.Error()) {
		t.Fatalf("error does not name the cutoff: %v", err)
	}
}
