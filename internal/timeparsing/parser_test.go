package timeparsing

import (
	"testing"
	"time"
)

// kiritimati is UTC+14 and newYork is UTC-5/-4: the two extremes #5823 used to
// show that a bare bound's meaning followed the host around.
var (
	kiritimati = time.FixedZone("Pacific/Kiritimati", 14*60*60)
	newYork    = time.FixedZone("America/New_York", -5*60*60)
)

// TestParseRelativeTimeInBareDateIgnoresHostZone is the #5823 regression: a
// bare YYYY-MM-DD bound is compared against created_at/updated_at/closed_at,
// which are stored in UTC, so it has to name the same instant no matter where
// the command runs. It used to resolve at the host's local midnight, which put
// a UTC+14 host 14 hours early and a UTC-5 host 5 hours late — enough to silently
// drop rows near the day boundary from a result that still looked complete.
func TestParseRelativeTimeInBareDateIgnoresHostZone(t *testing.T) {
	want := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)

	for name, zone := range map[string]*time.Location{
		"UTC":        time.UTC,
		"Kiritimati": kiritimati,
		"New_York":   newYork,
	} {
		t.Run(name, func(t *testing.T) {
			// now carries the host zone; only the literal's zone should matter.
			now := time.Date(2026, 8, 16, 20, 13, 18, 0, zone)
			got, err := ParseRelativeTimeIn("2026-08-17", now, time.UTC)
			if err != nil {
				t.Fatalf("ParseRelativeTimeIn: %v", err)
			}
			if !got.Equal(want) {
				t.Errorf("bare date resolved to %s, want %s (bound moved with the host zone)",
					got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

// TestParseRelativeTimeKeepsBareDateLocal guards the other half of the split.
// --due and --defer still go through ParseRelativeTime, where a bare date names
// a day on the user's calendar: "due 2026-09-01" must not become the evening of
// August 31 for anyone west of UTC.
func TestParseRelativeTimeKeepsBareDateLocal(t *testing.T) {
	now := time.Date(2026, 8, 16, 20, 13, 18, 0, newYork)

	got, err := ParseRelativeTime("2026-09-01", now)
	if err != nil {
		t.Fatalf("ParseRelativeTime: %v", err)
	}
	want := time.Date(2026, 9, 1, 0, 0, 0, 0, newYork)
	if !got.Equal(want) {
		t.Errorf("bare date resolved to %s, want local midnight %s",
			got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestParseRelativeTimeInLeavesRelativeFormsAlone pins that only the
// timezone-less absolute literals moved. An offset-bearing literal is already
// unambiguous and must be honored as written, and a duration names an instant
// relative to now that the literal zone has no business shifting.
func TestParseRelativeTimeInLeavesRelativeFormsAlone(t *testing.T) {
	now := time.Date(2026, 8, 16, 20, 13, 18, 0, newYork)

	t.Run("RFC3339 offset is honored", func(t *testing.T) {
		got, err := ParseRelativeTimeIn("2026-08-17T00:00:00-04:00", now, time.UTC)
		if err != nil {
			t.Fatalf("ParseRelativeTimeIn: %v", err)
		}
		want := time.Date(2026, 8, 17, 4, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("got %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
		}
	})

	t.Run("duration is unaffected by the literal zone", func(t *testing.T) {
		utc, err := ParseRelativeTimeIn("-1d", now, time.UTC)
		if err != nil {
			t.Fatalf("ParseRelativeTimeIn: %v", err)
		}
		local, err := ParseRelativeTimeIn("-1d", now, newYork)
		if err != nil {
			t.Fatalf("ParseRelativeTimeIn: %v", err)
		}
		if !utc.Equal(local) {
			t.Errorf("-1d resolved to %s under UTC but %s under New_York; a duration counts from now, not from the literal zone",
				utc.Format(time.RFC3339), local.Format(time.RFC3339))
		}
	})
}
