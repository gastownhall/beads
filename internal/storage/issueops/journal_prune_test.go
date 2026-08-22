package issueops

import (
	"errors"
	"testing"
	"time"
)

func neverCeil(t *testing.T) func() (int64, bool, error) {
	return func() (int64, bool, error) {
		t.Helper()
		t.Fatal("retain-rows floor is off; the ceiling read must not run")
		return 0, false, nil
	}
}

func neverDaysFloor(t *testing.T) func(time.Time) (int64, bool, error) {
	return func(time.Time) (int64, bool, error) {
		t.Helper()
		t.Fatal("retain-days floor is off; the age-floor read must not run")
		return 0, false, nil
	}
}

// TestComputeEventsPruneWhereResolvesToOnePrefixBound pins the two properties
// the retention floors exist for: every floor can only NARROW the bound, and
// the result is always a single `seq < N` — a strict prefix delete.
//
// The prefix shape is the load-bearing half. A per-row age predicate (which the
// reference implementation used) can delete seq N+1 while keeping seq N when
// client-stamped timestamps are not monotone in seq, and a hole in the middle
// of the retained window is silent loss the left-edge truncation check can
// never report.
func TestComputeEventsPruneWhereResolvesToOnePrefixBound(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	ceil := func(seq int64, found bool) func() (int64, bool, error) {
		return func() (int64, bool, error) { return seq, found, nil }
	}
	daysFloor := func(seq int64, found bool) func(time.Time) (int64, bool, error) {
		return func(time.Time) (int64, bool, error) { return seq, found, nil }
	}

	cases := []struct {
		name        string
		retainDays  int
		retainRows  int
		readCeil    func() (int64, bool, error)
		readDays    func(time.Time) (int64, bool, error)
		wantBound   int64
		wantSkip    bool
		wantNoCeil  bool
		wantNoFloor bool
	}{
		{
			name:        "both floors disabled: --before alone",
			wantBound:   100,
			wantNoCeil:  true,
			wantNoFloor: true,
		},
		{
			name:       "retain-days narrows to the oldest still-young seq",
			retainDays: 7,
			readDays:   daysFloor(42, true),
			wantBound:  42,
			wantNoCeil: true,
		},
		{
			name:       "retain-days with nothing young enough constrains nothing",
			retainDays: 7,
			readDays:   daysFloor(0, false),
			wantBound:  100,
			wantNoCeil: true,
		},
		{
			name:        "retain-rows narrows to one past the permitted ceiling",
			retainRows:  5,
			readCeil:    ceil(42, true),
			wantBound:   43,
			wantNoFloor: true,
		},
		{
			name:       "the tighter of the two floors wins",
			retainDays: 7,
			retainRows: 5,
			readCeil:   ceil(80, true),
			readDays:   daysFloor(42, true),
			wantBound:  42,
		},
		{
			name:       "a floor looser than --before never widens it",
			retainDays: 7,
			retainRows: 5,
			readCeil:   ceil(5000, true),
			readDays:   daysFloor(4000, true),
			wantBound:  100,
		},
		{
			name:        "journal smaller than retain-rows: nothing may be pruned",
			retainRows:  5,
			readCeil:    ceil(0, false),
			wantSkip:    true,
			wantNoFloor: true,
		},
		{
			name:       "the whole journal is inside the age window",
			retainDays: 7,
			readDays:   daysFloor(1, true),
			wantSkip:   true,
			wantNoCeil: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			readCeil := tc.readCeil
			if tc.wantNoCeil {
				readCeil = neverCeil(t)
			}
			readDays := tc.readDays
			if tc.wantNoFloor {
				readDays = neverDaysFloor(t)
			}
			where, args, skip, err := ComputeEventsPruneWhere(100, tc.retainDays, tc.retainRows, now, readCeil, readDays)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if skip != tc.wantSkip {
				t.Fatalf("skip = %v, want %v", skip, tc.wantSkip)
			}
			if skip {
				return
			}
			if where != "seq < ?" {
				t.Errorf("where = %q, want the single prefix clause %q — anything else can delete a row out of the middle", where, "seq < ?")
			}
			if len(args) != 1 || args[0] != tc.wantBound {
				t.Errorf("args = %v, want [%d]", args, tc.wantBound)
			}
		})
	}
}

// TestComputeEventsPruneWhereIgnoresTimestampOrdering is the regression guard
// for the clock-skew hole: the age floor is resolved to a seq, so a journal
// whose timestamps run backwards across a seq boundary either keeps the pair or
// drops the pair — it can never split one.
//
// The scenario is ordinary on a shared SQL server: two writers, or one writer
// whose clock is stepped back by NTP between two commits, so seq 5 carries a
// LATER ts than seq 6. Under a per-row `ts < cutoff` predicate seq 6 is deleted
// and seq 5 survives, leaving a hole above the retained floor.
func TestComputeEventsPruneWhereIgnoresTimestampOrdering(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	// The engine's MIN(seq) WHERE ts >= cutoff answers 5 for this journal: seq 5
	// is young (its clock ran ahead), seq 6 is old (the clock stepped back).
	// Resolving to a seq protects BOTH, because 6 sits above the floor.
	skewedFloor := func(cutoff time.Time) (int64, bool, error) {
		if !cutoff.Equal(now.AddDate(0, 0, -7).UTC()) {
			t.Errorf("cutoff = %s, want %s", cutoff, now.AddDate(0, 0, -7).UTC())
		}
		return 5, true, nil
	}

	_, args, skip, err := ComputeEventsPruneWhere(100, 7, 0, now, neverCeil(t), skewedFloor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skip {
		t.Fatal("prune skipped; want a bound at the age floor")
	}
	bound, ok := args[0].(int64)
	if !ok {
		t.Fatalf("bound arg is %T, want int64", args[0])
	}
	if bound != 5 {
		t.Fatalf("bound = %d, want 5", bound)
	}
	// The property, stated as the consumer sees it: seq 6 is older than the
	// cutoff but sits above the floor, so it survives with seq 5 rather than
	// being deleted out from under it.
	if bound > 6 {
		t.Errorf("bound %d deletes seq 6 while keeping seq 5 — a hole above the retained floor", bound)
	}
}

func TestComputeEventsPruneWhereSurfacesFloorReadFailures(t *testing.T) {
	now := time.Now().UTC()
	rowsErr := errors.New("ceiling unavailable")
	if _, _, _, err := ComputeEventsPruneWhere(100, 0, 5, now,
		func() (int64, bool, error) { return 0, false, rowsErr },
		neverDaysFloor(t),
	); !errors.Is(err, rowsErr) {
		t.Fatalf("err = %v, want the ceiling read error", err)
	}

	daysErr := errors.New("age floor unavailable")
	if _, _, _, err := ComputeEventsPruneWhere(100, 7, 0, now,
		neverCeil(t),
		func(time.Time) (int64, bool, error) { return 0, false, daysErr },
	); !errors.Is(err, daysErr) {
		t.Fatalf("err = %v, want the age floor read error", err)
	}
}
