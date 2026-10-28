package issueops

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const switchTestKey = "versioned-history.enabled"

var switchConfigRead = regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")

// switchResolver is a storage.VersionedHistoryResolver that reads switchTestKey
// through the readConfig it is handed and counts its calls, answering the
// parsed row or the read's error.
func switchResolver(calls *atomic.Int64) func(ctx context.Context, readConfig func(context.Context, string) (string, error)) (bool, error) {
	return func(ctx context.Context, readConfig func(context.Context, string) (string, error)) (bool, error) {
		calls.Add(1)
		v, err := readConfig(ctx, switchTestKey)
		if err != nil {
			return false, err
		}
		return v == "true", nil
	}
}

func switchMock(t *testing.T) (DBTX, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// TestVersionedHistorySwitchReadsTheRowOnceThroughTheFirstWrite pins the switch's
// contract: pending answers off without reading; the first ResolveIn reads the
// row through the handle it is given, once; every later call answers from the
// latch with no statement at all.
func TestVersionedHistorySwitchReadsTheRowOnceThroughTheFirstWrite(t *testing.T) {
	for _, tc := range []struct {
		row  *string
		want bool
	}{
		{row: nil, want: false},
		{row: switchRow("false"), want: false},
		{row: switchRow("true"), want: true},
	} {
		q, mock := switchMock(t)
		var calls atomic.Int64
		var sw VersionedHistorySwitch
		sw.Defer(switchResolver(&calls))
		if sw.Enabled() {
			t.Fatal("a pending switch reports on before anything was read")
		}
		rows := sqlmock.NewRows([]string{"value"})
		if tc.row != nil {
			rows.AddRow(*tc.row)
		}
		mock.ExpectQuery(switchConfigRead).WithArgs(switchTestKey).WillReturnRows(rows)
		for i := 0; i < 3; i++ {
			if got, err := sw.ResolveIn(context.Background(), q); err != nil || got != tc.want {
				t.Fatalf("row %v: ResolveIn #%d = (%v, %v), want (%v, nil)", tc.row, i+1, got, err, tc.want)
			}
		}
		if calls.Load() != 1 {
			t.Errorf("row %v: resolver called %d times over three writes, want 1", tc.row, calls.Load())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("row %v: %v", tc.row, err)
		}
		if sw.Enabled() != tc.want {
			t.Errorf("row %v: Enabled() = %v after resolving, want %v", tc.row, sw.Enabled(), tc.want)
		}
	}
}

// TestVersionedHistorySwitchAReadErrorIsNotAnAnswer: a failed read is handed
// back for the write to fail on, not answered off, and leaves the read pending,
// so the next write asks again.
func TestVersionedHistorySwitchAReadErrorIsNotAnAnswer(t *testing.T) {
	q, mock := switchMock(t)
	var calls atomic.Int64
	var sw VersionedHistorySwitch
	sw.Defer(switchResolver(&calls))
	readErr := errors.New("context canceled")
	mock.ExpectQuery(switchConfigRead).WithArgs(switchTestKey).WillReturnError(readErr)
	mock.ExpectQuery(switchConfigRead).WithArgs(switchTestKey).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("true"))
	if on, err := sw.ResolveIn(context.Background(), q); !errors.Is(err, readErr) || on {
		t.Fatalf("ResolveIn over a failed read = (%v, %v), want (false, the read's error) for the write to fail on", on, err)
	}
	if sw.Enabled() {
		t.Fatal("a failed read answered the switch on")
	}
	if on, err := sw.ResolveIn(context.Background(), q); err != nil || !on {
		t.Fatalf("the write after a failed read = (%v, %v), want (true, nil) from reading the row again", on, err)
	}
	if calls.Load() != 2 {
		t.Errorf("resolver called %d times, want 2", calls.Load())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// TestVersionedHistorySwitchOnlyEverTurnsOn: Set answers outright and drops a
// pending read; Defer leaves an enabled switch on and re-arms one that answered
// off, as a second open's re-read did.
func TestVersionedHistorySwitchOnlyEverTurnsOn(t *testing.T) {
	q, mock := switchMock(t)
	var calls atomic.Int64

	var on VersionedHistorySwitch
	on.Set(true)
	on.Defer(switchResolver(&calls))
	if enabled, err := on.ResolveIn(context.Background(), q); err != nil || !enabled || calls.Load() != 0 {
		t.Errorf("Defer on an enabled switch: on=%v, %d read(s); want on with no read", on.Enabled(), calls.Load())
	}

	var set VersionedHistorySwitch
	set.Defer(switchResolver(&calls))
	set.Set(false)
	if enabled, err := set.ResolveIn(context.Background(), q); err != nil || enabled || calls.Load() != 0 {
		t.Errorf("Set after Defer: on=%v, %d read(s); want off with no read", set.Enabled(), calls.Load())
	}

	var rearmed VersionedHistorySwitch
	mock.ExpectQuery(switchConfigRead).WithArgs(switchTestKey).WillReturnRows(sqlmock.NewRows([]string{"value"}))
	mock.ExpectQuery(switchConfigRead).WithArgs(switchTestKey).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("true"))
	rearmed.Defer(switchResolver(&calls))
	if enabled, err := rearmed.ResolveIn(context.Background(), q); err != nil || enabled {
		t.Fatalf("absent row resolved (%v, %v), want (false, nil)", enabled, err)
	}
	rearmed.Defer(switchResolver(&calls))
	if enabled, err := rearmed.ResolveIn(context.Background(), q); err != nil || !enabled {
		t.Fatalf("a switch that answered off was not re-armed by a second Defer: (%v, %v)", enabled, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// TestVersionedHistorySwitchConcurrentWritersReadOnce: writers racing on a pending
// switch make one read between them and all see its answer.
func TestVersionedHistorySwitchConcurrentWritersReadOnce(t *testing.T) {
	q, mock := switchMock(t)
	var calls atomic.Int64
	var sw VersionedHistorySwitch
	sw.Defer(switchResolver(&calls))
	mock.ExpectQuery(switchConfigRead).WithArgs(switchTestKey).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("true"))
	var wg sync.WaitGroup
	var offs atomic.Int64
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if enabled, err := sw.ResolveIn(context.Background(), q); err != nil || !enabled {
				offs.Add(1)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || offs.Load() != 0 {
		t.Errorf("16 racing writers: %d read(s), %d answered off; want 1 read and none off", calls.Load(), offs.Load())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// TestVersionedHistorySwitchNoOffWindowWhileAnswering pins the publication
// order in ResolveIn and Set: a writer on the lock-free path must never see
// the pending flag cleared before the answer is published. Each trial starts
// eight writers against a switch that answers on; the ones arriving while
// another is answering take the lock-free path, and any false there is a
// write scoped off while the row says on.
func TestVersionedHistorySwitchNoOffWindowWhileAnswering(t *testing.T) {
	alwaysOn := func(context.Context, func(context.Context, string) (string, error)) (bool, error) {
		return true, nil
	}
	ctx := context.Background()
	const trials = 20000 // about 0.2s
	var offs atomic.Int64
	for trial := 0; trial < trials; trial++ {
		var sw VersionedHistorySwitch
		sw.Defer(alwaysOn)
		var gate, done sync.WaitGroup
		gate.Add(1)
		for g := 0; g < 8; g++ {
			done.Add(1)
			go func() {
				defer done.Done()
				gate.Wait()
				for i := 0; i < 50; i++ {
					if enabled, err := sw.ResolveIn(ctx, nil); err != nil || !enabled {
						offs.Add(1)
					}
				}
			}()
		}
		gate.Done()
		done.Wait()
	}
	if n := offs.Load(); n != 0 {
		t.Fatalf("%d ResolveIn call(s) answered off from a switch whose row says on", n)
	}

	// Set(true) on a pending switch must publish the same way.
	for trial := 0; trial < trials/10; trial++ {
		var sw VersionedHistorySwitch
		sw.Defer(alwaysOn)
		var gate, done sync.WaitGroup
		gate.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			gate.Wait()
			sw.Set(true)
		}()
		for g := 0; g < 4; g++ {
			done.Add(1)
			go func() {
				defer done.Done()
				gate.Wait()
				for i := 0; i < 50; i++ {
					// The fast path's two loads, in its order: once
					// hasPending is clear, enabled must be the answer.
					if !sw.hasPending.Load() && !sw.enabled.Load() {
						offs.Add(1)
					}
				}
			}()
		}
		gate.Done()
		done.Wait()
	}
	if n := offs.Load(); n != 0 {
		t.Fatalf("%d observation(s) of a cleared pending flag before Set published on", n)
	}
}

func switchRow(s string) *string { return &s }
