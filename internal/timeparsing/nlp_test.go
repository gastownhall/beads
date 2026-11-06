package timeparsing

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestParseNaturalLanguage tests the NLP parser wrapper.
func TestParseNaturalLanguage(t *testing.T) {
	// Fixed reference time: Wednesday, January 15, 2025, 10:00:00 AM
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.Local)

	tests := []struct {
		name      string
		input     string
		wantYear  int
		wantMonth time.Month
		wantDay   int
		wantHour  int // -1 means don't check hour
		wantErr   bool
	}{
		// Relative days
		{
			name:      "tomorrow",
			input:     "tomorrow",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   16,
			wantHour:  -1,
			wantErr:   false,
		},
		{
			name:      "yesterday",
			input:     "yesterday",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   14,
			wantHour:  -1,
			wantErr:   false,
		},

		// Next weekday (reference is Wednesday Jan 15)
		{
			name:      "next monday",
			input:     "next monday",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   20, // Next Monday after Jan 15
			wantHour:  -1,
			wantErr:   false,
		},
		{
			name:      "next friday",
			input:     "next friday",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   17, // Friday Jan 17 (same week)
			wantHour:  -1,
			wantErr:   false,
		},

		// With time
		{
			name:      "tomorrow at 9am",
			input:     "tomorrow at 9am",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   16,
			wantHour:  9,
			wantErr:   false,
		},
		{
			name:      "next monday at 2pm",
			input:     "next monday at 2pm",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   20,
			wantHour:  14,
			wantErr:   false,
		},

		// Relative durations (NLP style)
		{
			name:      "in 3 days",
			input:     "in 3 days",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   18,
			wantHour:  -1,
			wantErr:   false,
		},
		{
			name:      "in 1 week",
			input:     "in 1 week",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   22,
			wantHour:  -1,
			wantErr:   false,
		},

		// Past relative
		{
			name:      "3 days ago",
			input:     "3 days ago",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   12,
			wantHour:  -1,
			wantErr:   false,
		},

		// Invalid inputs
		{
			name:    "random text",
			input:   "not a date at all",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNaturalLanguage(tt.input, now)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseNaturalLanguage(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			if got.Year() != tt.wantYear {
				t.Errorf("parseNaturalLanguage(%q) year = %d, want %d", tt.input, got.Year(), tt.wantYear)
			}
			if got.Month() != tt.wantMonth {
				t.Errorf("parseNaturalLanguage(%q) month = %v, want %v", tt.input, got.Month(), tt.wantMonth)
			}
			if got.Day() != tt.wantDay {
				t.Errorf("parseNaturalLanguage(%q) day = %d, want %d", tt.input, got.Day(), tt.wantDay)
			}
			if tt.wantHour >= 0 && got.Hour() != tt.wantHour {
				t.Errorf("parseNaturalLanguage(%q) hour = %d, want %d", tt.input, got.Hour(), tt.wantHour)
			}
		})
	}
}

// TestParseRelativeTime tests the layered parsing function.
func TestParseRelativeTime(t *testing.T) {
	// Fixed reference time: Wednesday, January 15, 2025, 10:00:00 AM
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		input     string
		wantYear  int
		wantMonth time.Month
		wantDay   int
		wantHour  int // -1 means don't check hour
		wantErr   bool
	}{
		// Layer 1: Compact duration (should be tried first)
		{
			name:      "compact +1d",
			input:     "+1d",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   16,
			wantHour:  10, // Same hour as now
			wantErr:   false,
		},
		{
			name:      "compact +6h",
			input:     "+6h",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   15,
			wantHour:  16, // 10 + 6 = 16
			wantErr:   false,
		},

		// Layer 2: NLP
		{
			name:      "NLP tomorrow",
			input:     "tomorrow",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   16,
			wantHour:  -1,
			wantErr:   false,
		},
		{
			name:      "NLP next monday",
			input:     "next monday",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   20,
			wantHour:  -1,
			wantErr:   false,
		},

		// Layer 3: Date-only
		{
			name:      "date-only",
			input:     "2025-02-01",
			wantYear:  2025,
			wantMonth: time.February,
			wantDay:   1,
			wantHour:  0,
			wantErr:   false,
		},

		// Layer 4: RFC3339
		{
			name:      "RFC3339",
			input:     "2025-03-15T14:30:00Z",
			wantYear:  2025,
			wantMonth: time.March,
			wantDay:   15,
			wantHour:  14,
			wantErr:   false,
		},

		// Invalid
		{
			name:    "invalid expression",
			input:   "not-a-date",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRelativeTime(tt.input, now)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseRelativeTime(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			if got.Year() != tt.wantYear {
				t.Errorf("ParseRelativeTime(%q) year = %d, want %d", tt.input, got.Year(), tt.wantYear)
			}
			if got.Month() != tt.wantMonth {
				t.Errorf("ParseRelativeTime(%q) month = %v, want %v", tt.input, got.Month(), tt.wantMonth)
			}
			if got.Day() != tt.wantDay {
				t.Errorf("ParseRelativeTime(%q) day = %d, want %d", tt.input, got.Day(), tt.wantDay)
			}
			if tt.wantHour >= 0 && got.Hour() != tt.wantHour {
				t.Errorf("ParseRelativeTime(%q) hour = %d, want %d", tt.input, got.Hour(), tt.wantHour)
			}
		})
	}
}

// TestParseRelativeTime_LayerPrecedence verifies that layers are tried in order.
func TestParseRelativeTime_LayerPrecedence(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	// "+1d" is valid compact duration, should NOT be parsed as NLP
	t1, err := ParseRelativeTime("+1d", now)
	if err != nil {
		t.Fatalf("ParseRelativeTime(\"+1d\") failed: %v", err)
	}
	// Compact duration adds exactly 1 day, preserving time
	expected := now.AddDate(0, 0, 1)
	if !t1.Equal(expected) {
		t.Errorf("ParseRelativeTime(\"+1d\") = %v, want %v (compact duration should take precedence)", t1, expected)
	}

	// "2025-01-20" should parse as date-only, not NLP
	t2, err := ParseRelativeTime("2025-01-20", now)
	if err != nil {
		t.Fatalf("ParseRelativeTime(\"2025-01-20\") failed: %v", err)
	}
	if t2.Day() != 20 || t2.Month() != time.January || t2.Year() != 2025 {
		t.Errorf("ParseRelativeTime(\"2025-01-20\") = %v, want Jan 20, 2025", t2)
	}
}

func TestParseRelativeTimeNormalizesToUTC(t *testing.T) {
	loc := time.FixedZone("UTC-6", -6*60*60)
	now := time.Date(2026, 7, 31, 21, 40, 15, 0, loc)

	tests := []struct {
		input string
		want  time.Time
	}{
		{input: "+1h", want: time.Date(2026, 8, 1, 4, 40, 15, 0, time.UTC)},
		{input: "2026-08-01", want: time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)},
		{input: "2026-08-01T09:00:00", want: time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC)},
		{input: "2026-08-01 09:00:00", want: time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC)},
		{input: "2026-08-01T09:00:00-06:00", want: time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC)},
		{input: "tomorrow at 9am", want: time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC)},
		// GH#8167: zone-less datetimes without seconds, both separators.
		{input: "2026-08-01 09:00", want: time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC)},
		{input: "2026-08-01T09:00", want: time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC)},
		{input: "2026-08-01 9:00", want: time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC)},
		{input: "2026-08-01T9:00", want: time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseRelativeTime(tt.input, now)
			if err != nil {
				t.Fatalf("ParseRelativeTime(%q) failed: %v", tt.input, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("ParseRelativeTime(%q) = %v, want %v", tt.input, got, tt.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("ParseRelativeTime(%q) location = %v, want UTC", tt.input, got.Location())
			}
		})
	}
}

// TestParseRelativeTimeDatetimeWithoutSeconds covers GH#8167: a datetime
// without seconds must parse as that date and time in now's location, for
// both the space and the T separator, never fall through to the NLP layer.
func TestParseRelativeTimeDatetimeWithoutSeconds(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "UTC-5",
			now:  time.Date(2026, 11, 6, 6, 7, 0, 0, time.FixedZone("UTC-5", -5*60*60)),
			want: time.Date(2026, 11, 7, 13, 30, 0, 0, time.UTC),
		},
		{
			// The reported case: host in UTC, the day before the target date.
			name: "UTC",
			now:  time.Date(2026, 11, 6, 18, 0, 0, 0, time.UTC),
			want: time.Date(2026, 11, 7, 8, 30, 0, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		for _, input := range []string{"2026-11-07 08:30", "2026-11-07T08:30"} {
			t.Run(tc.name+"/"+input, func(t *testing.T) {
				got, err := ParseRelativeTime(input, tc.now)
				if err != nil {
					t.Fatalf("ParseRelativeTime(%q) failed: %v", input, err)
				}
				if !got.Equal(tc.want) {
					t.Errorf("ParseRelativeTime(%q) = %v, want %v", input, got, tc.want)
				}
				if got.Location() != time.UTC {
					t.Errorf("ParseRelativeTime(%q) location = %v, want UTC", input, got.Location())
				}
			})
		}
	}
}

// TestParseRelativeTimeRejectsMalformedDatePrefix pins that input starting
// with a YYYY-MM-DD date which no absolute layout accepts is an error, never
// handed to the NLP layer, which would keep a clock-like fragment of it and
// drop the date (GH#8167).
func TestParseRelativeTimeRejectsMalformedDatePrefix(t *testing.T) {
	now := time.Date(2026, 11, 6, 18, 0, 0, 0, time.UTC)

	inputs := []string{
		// Invalid calendar dates.
		"2026-02-30",
		"2026-11-31",
		"2026-13-01",
		// Invalid or unsupported time suffixes.
		"2026-11-07 24:00",
		"2026-11-07 08:60",
		"2026-11-07 8.30",
		"2026-11-07 08:30 PM",
		"2026-11-07 at 9am",
		"2026-11-07 08:30Z",
		// T separator and trailing junk.
		"2026-11-07T24:00",
		"2026-11-07T08:30 PM",
		"2026-11-07T08:30:00 tomorrow",
		"2026-11-07x",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			got, err := ParseRelativeTime(input, now)
			if err == nil {
				t.Fatalf("ParseRelativeTime(%q) = %v, want an error", input, got)
			}
			if !got.IsZero() {
				t.Errorf("ParseRelativeTime(%q) time = %v, want zero", input, got)
			}
			if want := "cannot parse time expression: " + strconv.Quote(input); !strings.Contains(err.Error(), want) {
				t.Errorf("ParseRelativeTime(%q) error = %q, want it to contain %q", input, err, want)
			}
		})
	}
}

// TestParseRelativeTimeKeepsAcceptedForms pins forms that parsed before the
// minute layouts and the leading-ISO-date guard (GH#8167) so they keep their
// exact instants: the guard runs only after every absolute layout fails.
func TestParseRelativeTimeKeepsAcceptedForms(t *testing.T) {
	utcNow := time.Date(2026, 11, 6, 18, 0, 0, 0, time.UTC)
	loc := time.FixedZone("UTC-6", -6*60*60)
	zonedNow := time.Date(2026, 7, 31, 21, 40, 15, 0, loc)

	tests := []struct {
		input string
		now   time.Time
		want  time.Time
	}{
		// Compact durations.
		{input: "+30min", now: utcNow, want: time.Date(2026, 11, 6, 18, 30, 0, 0, time.UTC)},
		{input: "-1d", now: utcNow, want: time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC)},
		{input: "+2w", now: zonedNow, want: time.Date(2026, 8, 15, 3, 40, 15, 0, time.UTC)},
		// Valid date-only.
		{input: "2026-11-07", now: utcNow, want: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)},
		{input: "2026-08-01", now: zonedNow, want: time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)},
		// Zone-less forms with seconds.
		{input: "2026-11-07 08:30:00", now: utcNow, want: time.Date(2026, 11, 7, 8, 30, 0, 0, time.UTC)},
		{input: "2026-11-07T08:30:00", now: zonedNow, want: time.Date(2026, 11, 7, 14, 30, 0, 0, time.UTC)},
		// Fractional seconds, accepted by the seconds layouts.
		{input: "2026-11-07 08:30:00.5", now: utcNow, want: time.Date(2026, 11, 7, 8, 30, 0, 500_000_000, time.UTC)},
		{input: "2026-11-07T08:30:00.5", now: utcNow, want: time.Date(2026, 11, 7, 8, 30, 0, 500_000_000, time.UTC)},
		// RFC3339 with an offset or Z.
		{input: "2026-11-07T13:30:00Z", now: utcNow, want: time.Date(2026, 11, 7, 13, 30, 0, 0, time.UTC)},
		{input: "2026-11-07T08:30:00-05:00", now: utcNow, want: time.Date(2026, 11, 7, 13, 30, 0, 0, time.UTC)},
		// Natural language.
		{input: "tomorrow at 9am", now: utcNow, want: time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)},
		{input: "tomorrow at 9am", now: zonedNow, want: time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC)},
	}

	for _, tt := range tests {
		t.Run(tt.now.Location().String()+"/"+tt.input, func(t *testing.T) {
			got, err := ParseRelativeTime(tt.input, tt.now)
			if err != nil {
				t.Fatalf("ParseRelativeTime(%q) failed: %v", tt.input, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("ParseRelativeTime(%q) = %v, want %v", tt.input, got, tt.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("ParseRelativeTime(%q) location = %v, want UTC", tt.input, got.Location())
			}
		})
	}
}
