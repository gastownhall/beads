package timeparsing

import (
	"testing"
	"time"
)

func mustParseRepeat(t *testing.T, s string) Repeat {
	t.Helper()
	r, err := ParseRepeat(s)
	if err != nil {
		t.Fatalf("ParseRepeat(%q) error = %v, want nil", s, err)
	}
	return r
}

func TestParseRepeatEmptyIsZero(t *testing.T) {
	r, err := ParseRepeat("   ")
	if err != nil {
		t.Fatalf("ParseRepeat(blank) error = %v, want nil", err)
	}
	if !r.IsZero() {
		t.Errorf("ParseRepeat(blank).IsZero() = false, want true")
	}
	if _, err := r.Next(time.Now()); err == nil {
		t.Errorf("zero Repeat.Next() error = nil, want an error")
	}
}

func TestParseRepeatInterval(t *testing.T) {
	base := time.Date(2026, 3, 10, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		pattern string
		want    time.Time
	}{
		{"+1d", base.AddDate(0, 0, 1)},
		{"2d", base.AddDate(0, 0, 2)},
		{"+1w", base.AddDate(0, 0, 7)},
		{"+6h", base.Add(6 * time.Hour)},
		{"+1m", base.AddDate(0, 1, 0)},
		{"+1y", base.AddDate(1, 0, 0)},
	}
	for _, tc := range tests {
		got, err := mustParseRepeat(t, tc.pattern).Next(base)
		if err != nil {
			t.Fatalf("%s: Next error = %v", tc.pattern, err)
		}
		if !got.Equal(tc.want) {
			t.Errorf("%s: Next = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

func ymd(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 9, 0, 0, 0, time.UTC) }

// A monthly or yearly series keeps the ORIGINAL anchor day: each step clamps
// to the target month's length, and an anchor that is itself a month end is a
// month-end series. The anchor is what the series was first scheduled on, not
// whatever a short month clamped the previous occurrence to.
func TestNextFromKeepsTheAnchorDay(t *testing.T) {
	monthly := mustParseRepeat(t, "+1m")
	yearly := mustParseRepeat(t, "+1y")
	tests := []struct {
		name   string
		rule   Repeat
		anchor time.Time
		want   []time.Time
	}{
		{"Jan 30 clamps to Feb and returns to the 30th", monthly, ymd(2026, 1, 30),
			[]time.Time{ymd(2026, 2, 28), ymd(2026, 3, 30), ymd(2026, 4, 30), ymd(2026, 5, 30)}},
		{"Apr 30 stays on the 30th, not on month ends", monthly, ymd(2026, 4, 30),
			[]time.Time{ymd(2026, 5, 30), ymd(2026, 6, 30), ymd(2026, 7, 30)}},
		{"Jan 31 is a month-end series", monthly, ymd(2026, 1, 31),
			[]time.Time{ymd(2026, 2, 28), ymd(2026, 3, 31), ymd(2026, 4, 30), ymd(2026, 5, 31)}},
		{"a mid-month anchor is untouched", monthly, ymd(2026, 1, 15),
			[]time.Time{ymd(2026, 2, 15), ymd(2026, 3, 15)}},
		{"a leap-day anchor returns to Feb 29 when the calendar has one", yearly, ymd(2028, 2, 29),
			[]time.Time{ymd(2029, 2, 28), ymd(2030, 2, 28), ymd(2031, 2, 28), ymd(2032, 2, 29)}},
		{"Jan 30 in a leap year", monthly, ymd(2028, 1, 30),
			[]time.Time{ymd(2028, 2, 29), ymd(2028, 3, 30)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cur := tc.anchor
			for _, want := range tc.want {
				next, err := tc.rule.NextFrom(cur, tc.anchor)
				if err != nil {
					t.Fatalf("NextFrom(%v, anchor %v) error = %v", cur, tc.anchor, err)
				}
				if !next.Equal(want) {
					t.Fatalf("NextFrom(%v, anchor %v) = %v, want %v", cur, tc.anchor, next, want)
				}
				cur = next
			}
		})
	}

	// Without an anchor, Next only clamps; it does not promote a clamped
	// occurrence to a month-end series.
	plain, err := monthly.Next(ymd(2026, 2, 28))
	if err != nil || !plain.Equal(ymd(2026, 3, 28)) {
		t.Errorf("Next(Feb 28) = %v, %v; want Mar 28", plain, err)
	}
	// Non-calendar rules ignore the anchor entirely.
	weekly := mustParseRepeat(t, "+1w")
	w, err := weekly.NextFrom(ymd(2026, 3, 2), ymd(2026, 1, 31))
	if err != nil || !w.Equal(ymd(2026, 3, 9)) {
		t.Errorf("weekly NextFrom = %v, %v; want Mar 9", w, err)
	}
}

func TestParseRepeatRejectsBadPatterns(t *testing.T) {
	for _, pattern := range []string{
		"-1d",         // backwards: no next occurrence
		"0d",          // zero interval: would never advance
		"weekly",      // not an interval and not cron
		"0 9 * *",     // four fields
		"0 9 * * * *", // six fields
		"60 9 * * *",  // minute out of range
		"0 24 * * *",  // hour out of range
		"0 9 32 * *",  // day-of-month out of range
		"0 9 * 13 *",  // month out of range
		"0 9 * * 8",   // day-of-week out of range
		"0 9 * * MOO", // unknown day name
		"0 9 30 2 *",  // February 30th never occurs
		"*/0 * * * *", // zero step
		"0,, 9 * * *", // empty list element
	} {
		if _, err := ParseRepeat(pattern); err == nil {
			t.Errorf("ParseRepeat(%q) error = nil, want an error", pattern)
		}
	}
}

func TestCronNextBasicSchedules(t *testing.T) {
	// 2026-03-10 is a Tuesday.
	base := time.Date(2026, 3, 10, 9, 30, 15, 0, time.UTC)
	tests := []struct {
		name    string
		pattern string
		want    time.Time
	}{
		{"every minute drops sub-minute precision", "* * * * *", time.Date(2026, 3, 10, 9, 31, 0, 0, time.UTC)},
		{"daily at 09:00 rolls to tomorrow", "0 9 * * *", time.Date(2026, 3, 11, 9, 0, 0, 0, time.UTC)},
		{"daily at 18:00 same day", "0 18 * * *", time.Date(2026, 3, 10, 18, 0, 0, 0, time.UTC)},
		{"weekly Monday 09:00", "0 9 * * 1", time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)},
		{"weekly Monday by name", "0 9 * * MON", time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)},
		{"monthly on the 1st", "0 0 1 * *", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{"yearly on Jan 1", "0 0 1 1 *", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"yearly by month name", "0 0 1 JAN *", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"every 15 minutes", "*/15 * * * *", time.Date(2026, 3, 10, 9, 45, 0, 0, time.UTC)},
		{"range of hours", "0 10-12 * * *", time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC)},
		{"list of minutes", "0,20,40 * * * *", time.Date(2026, 3, 10, 9, 40, 0, 0, time.UTC)},
		{"leap day", "0 0 29 2 *", time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mustParseRepeat(t, tc.pattern).Next(base)
			if err != nil {
				t.Fatalf("Next error = %v", err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("Next = %v, want %v", got, tc.want)
			}
		})
	}
}

// A match exactly at t must advance: Next is strictly-after, so a recurring
// bead closed at its own due minute does not respawn onto the same instant.
func TestCronNextIsStrictlyAfter(t *testing.T) {
	at := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	got, err := mustParseRepeat(t, "0 9 * * *").Next(at)
	if err != nil {
		t.Fatalf("Next error = %v", err)
	}
	if want := at.AddDate(0, 0, 1); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

// Vixie rule: with BOTH day-of-month and day-of-week restricted, either
// matching selects the day.
func TestCronDayOfMonthOrDayOfWeek(t *testing.T) {
	base := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC) // Tuesday the 10th
	// The 15th (a Sunday) or any Friday, whichever comes first: Friday 13th.
	got, err := mustParseRepeat(t, "0 0 15 * 5").Next(base)
	if err != nil {
		t.Fatalf("Next error = %v", err)
	}
	if want := time.Date(2026, 3, 13, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

func TestRepeatStringRoundTrips(t *testing.T) {
	for _, pattern := range []string{"+1w", "0 9 * * 1"} {
		if got := mustParseRepeat(t, pattern).String(); got != pattern {
			t.Errorf("String() = %q, want %q", got, pattern)
		}
	}
}

// Successive Next calls must walk forward, never stall on one occurrence.
func TestCronNextAdvancesAcrossOccurrences(t *testing.T) {
	r := mustParseRepeat(t, "0 9 * * 1-5")
	cur := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	prev := cur
	for i := 0; i < 10; i++ {
		next, err := r.Next(cur)
		if err != nil {
			t.Fatalf("iteration %d: Next error = %v", i, err)
		}
		if !next.After(prev) {
			t.Fatalf("iteration %d: Next = %v did not advance past %v", i, next, prev)
		}
		if wd := next.Weekday(); wd == time.Saturday || wd == time.Sunday {
			t.Fatalf("iteration %d: Next = %v landed on a weekend", i, next)
		}
		prev, cur = next, next
	}
}

// FirstAtOrAfter is the "series has not started yet" entry point: for an
// interval it is t itself (a weekly series starting Monday starts ON Monday,
// not a week later); for cron it is the first matching minute at or after t.
func TestFirstAtOrAfter(t *testing.T) {
	start := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC) // a Monday
	t.Run("interval starts on the bound", func(t *testing.T) {
		got, err := mustParseRepeat(t, "+1w").FirstAtOrAfter(start)
		if err != nil {
			t.Fatalf("FirstAtOrAfter error = %v", err)
		}
		if !got.Equal(start) {
			t.Errorf("FirstAtOrAfter = %v, want %v", got, start)
		}
	})
	t.Run("cron match exactly on the bound is kept", func(t *testing.T) {
		got, err := mustParseRepeat(t, "0 9 * * 1").FirstAtOrAfter(start)
		if err != nil {
			t.Fatalf("FirstAtOrAfter error = %v", err)
		}
		if !got.Equal(start) {
			t.Errorf("FirstAtOrAfter = %v, want %v", got, start)
		}
	})
	t.Run("cron bound mid-minute advances", func(t *testing.T) {
		got, err := mustParseRepeat(t, "* * * * *").FirstAtOrAfter(start.Add(30 * time.Second))
		if err != nil {
			t.Fatalf("FirstAtOrAfter error = %v", err)
		}
		if want := start.Add(time.Minute); !got.Equal(want) {
			t.Errorf("FirstAtOrAfter = %v, want %v", got, want)
		}
	})
	t.Run("zero rule refuses", func(t *testing.T) {
		if _, err := (Repeat{}).FirstAtOrAfter(start); err == nil {
			t.Error("FirstAtOrAfter on the zero Repeat = nil error, want an error")
		}
	})
}
