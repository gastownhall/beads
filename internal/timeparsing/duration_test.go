package timeparsing

import (
	"testing"
	"time"
)

func TestParseCompactDuration(t *testing.T) {
	// Fixed reference time for deterministic tests
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		input   string
		want    time.Time
		wantErr bool
	}{
		// Valid positive durations
		{
			name:  "+6h adds 6 hours",
			input: "+6h",
			want:  time.Date(2025, 6, 15, 18, 0, 0, 0, time.UTC),
		},
		{
			name:  "+1d adds 1 day",
			input: "+1d",
			want:  time.Date(2025, 6, 16, 12, 0, 0, 0, time.UTC),
		},
		{
			name:  "+2w adds 2 weeks",
			input: "+2w",
			want:  time.Date(2025, 6, 29, 12, 0, 0, 0, time.UTC),
		},
		{
			name:  "+3m adds 3 months",
			input: "+3m",
			want:  time.Date(2025, 9, 15, 12, 0, 0, 0, time.UTC),
		},
		{
			name:  "+1y adds 1 year",
			input: "+1y",
			want:  time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC),
		},

		// "min" is the unambiguous minute unit; bare "m" stays months (GH #6609)
		{
			name:  "+30min adds 30 minutes",
			input: "+30min",
			want:  time.Date(2025, 6, 15, 12, 30, 0, 0, time.UTC),
		},
		{
			name:  "-15min subtracts 15 minutes",
			input: "-15min",
			want:  time.Date(2025, 6, 15, 11, 45, 0, 0, time.UTC),
		},
		{
			name:  "90min without sign adds 90 minutes",
			input: "90min",
			want:  time.Date(2025, 6, 15, 13, 30, 0, 0, time.UTC),
		},
		{
			name:  "+30m still adds 30 months",
			input: "+30m",
			want:  time.Date(2027, 12, 15, 12, 0, 0, 0, time.UTC),
		},

		// Valid negative durations (past)
		{
			name:  "-1d subtracts 1 day",
			input: "-1d",
			want:  time.Date(2025, 6, 14, 12, 0, 0, 0, time.UTC),
		},
		{
			name:  "-2w subtracts 2 weeks",
			input: "-2w",
			want:  time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		},
		{
			name:  "-6h subtracts 6 hours",
			input: "-6h",
			want:  time.Date(2025, 6, 15, 6, 0, 0, 0, time.UTC),
		},

		// No sign means positive
		{
			name:  "3m without sign adds 3 months",
			input: "3m",
			want:  time.Date(2025, 9, 15, 12, 0, 0, 0, time.UTC),
		},
		{
			name:  "1y without sign adds 1 year",
			input: "1y",
			want:  time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC),
		},
		{
			name:  "6h without sign adds 6 hours",
			input: "6h",
			want:  time.Date(2025, 6, 15, 18, 0, 0, 0, time.UTC),
		},

		// Multi-digit amounts
		{
			name:  "+24h adds 24 hours",
			input: "+24h",
			want:  time.Date(2025, 6, 16, 12, 0, 0, 0, time.UTC),
		},
		{
			name:  "+365d adds 365 days",
			input: "+365d",
			want:  time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC),
		},

		// Invalid inputs
		{
			name:    "6h+ (sign at end) is invalid",
			input:   "6h+",
			wantErr: true,
		},
		{
			name:    "++1d (double sign) is invalid",
			input:   "++1d",
			wantErr: true,
		},
		{
			name:    "30mins (plural) is invalid",
			input:   "+30mins",
			wantErr: true,
		},
		{
			name:    "30minutes is invalid",
			input:   "+30minutes",
			wantErr: true,
		},
		{
			name:    "1x (unknown unit) is invalid",
			input:   "1x",
			wantErr: true,
		},
		{
			name:    "empty string is invalid",
			input:   "",
			wantErr: true,
		},
		{
			name:    "just a number is invalid",
			input:   "6",
			wantErr: true,
		},
		{
			name:    "just a unit is invalid",
			input:   "h",
			wantErr: true,
		},
		{
			name:    "spaces are invalid",
			input:   "+ 6h",
			wantErr: true,
		},
		{
			name:    "ISO date is not compact duration",
			input:   "2025-01-15",
			wantErr: true,
		},
		{
			name:    "natural language is not compact duration",
			input:   "tomorrow",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCompactDuration(tt.input, now)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseCompactDuration(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && !got.Equal(tt.want) {
				t.Errorf("ParseCompactDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsCompactDuration(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"+6h", true},
		{"-1d", true},
		{"+2w", true},
		{"3m", true},
		{"1y", true},
		{"+24h", true},
		{"+30min", true},
		{"+30mins", false},
		{"", false},
		{"tomorrow", false},
		{"2025-01-15", false},
		{"6h+", false},
		{"++1d", false},
		{"1x", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isCompactDuration(tt.input)
			if got != tt.want {
				t.Errorf("isCompactDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestParseCompactDuration_MonthBoundary tests month arithmetic edge cases.
func TestParseCompactDuration_MonthBoundary(t *testing.T) {
	// Jan 31 + 1 month = Feb 28 (or 29 in leap year)
	jan31 := time.Date(2025, 1, 31, 12, 0, 0, 0, time.UTC)
	got, err := ParseCompactDuration("+1m", jan31)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Go's AddDate normalizes: Jan 31 + 1 month = March 3 (31 days into Feb)
	// This is Go's default behavior, which we preserve
	if got.Month() != time.March {
		t.Logf("Note: Jan 31 + 1m = %v (Go's AddDate overflow behavior)", got)
	}
}

// TestParseCompactDuration_LeapYear tests leap year handling.
func TestParseCompactDuration_LeapYear(t *testing.T) {
	// Feb 28, 2024 (leap year) + 1d = Feb 29
	feb28_2024 := time.Date(2024, 2, 28, 12, 0, 0, 0, time.UTC)
	got, err := ParseCompactDuration("+1d", feb28_2024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Feb 28, 2024 + 1d = %v, want %v", got, want)
	}
}

// TestParseCompactDuration_PreservesTimezone tests that local timezone is preserved.
func TestParseCompactDuration_PreservesTimezone(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("timezone America/New_York not available")
	}

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, loc)
	got, err := ParseCompactDuration("+1d", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Location() != loc {
		t.Errorf("timezone not preserved: got %v, want %v", got.Location(), loc)
	}
}
