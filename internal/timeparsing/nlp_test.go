package timeparsing

import (
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
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.Local)

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
