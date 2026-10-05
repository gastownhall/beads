package main

import (
	"testing"
	"time"
)

// TestWispIsAbandoned pins the #7236 fix: --age 0s (ageThreshold <= 0) must
// treat every wisp as abandoned regardless of its updatedAt, including a
// wisp whose stored updatedAt reads back AFTER now — the shape Dolt's
// DATETIME(0) half-up rounding on write produces for a wisp created in the
// second half of a wall-clock second (#7236). Before this fix, that case
// failed the strict now.Sub(updatedAt) > ageThreshold comparison because
// the difference is negative.
func TestWispIsAbandoned(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		updatedAt time.Time
		age       time.Duration
		want      bool
	}{
		{
			name:      "age 0s: a wisp updated exactly now is abandoned",
			updatedAt: now,
			age:       0,
			want:      true,
		},
		{
			name:      "age 0s: a wisp whose stored updatedAt rounded into the future is still abandoned",
			updatedAt: now.Add(500 * time.Millisecond),
			age:       0,
			want:      true,
		},
		{
			name:      "a negative age behaves like zero",
			updatedAt: now.Add(500 * time.Millisecond),
			age:       -time.Second,
			want:      true,
		},
		{
			name:      "positive age: a fresh wisp is not abandoned",
			updatedAt: now,
			age:       time.Hour,
			want:      false,
		},
		{
			name:      "positive age: a wisp past the threshold is abandoned",
			updatedAt: now.Add(-2 * time.Hour),
			age:       time.Hour,
			want:      true,
		},
		{
			name:      "positive age: exactly at the threshold is not abandoned (strict >, unchanged)",
			updatedAt: now.Add(-time.Hour),
			age:       time.Hour,
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wispIsAbandoned(now, tc.updatedAt, tc.age); got != tc.want {
				t.Errorf("wispIsAbandoned(%v, %v, %v) = %v, want %v", now, tc.updatedAt, tc.age, got, tc.want)
			}
		})
	}
}
