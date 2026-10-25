package main

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/issueops"
)

// TestParseOlderThan pins --older-than on both `bd purge` and `bd prune`.
// Day-denominated values keep their meaning; hour (and finer) values are taken
// exactly rather than floored to whole days — "36h" used to become 24h and
// sweep rows younger than the caller asked for.
func TestParseOlderThan(t *testing.T) {
	const day = 24 * time.Hour
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		// Unchanged day semantics.
		{"7", 7 * day, false},
		{"30", 30 * day, false},
		{"7d", 7 * day, false},
		{"30d", 30 * day, false},
		{"2w", 14 * day, false},
		{"1w", 7 * day, false},
		{"7D", 7 * day, false},
		{"2W", 14 * day, false},
		// Hour precision, no longer floored to days.
		{"48h", 48 * time.Hour, false},
		{"168h", 7 * day, false},
		{"36h", 36 * time.Hour, false},
		{"12h", 12 * time.Hour, false},
		{"36H", 36 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"1h30m", 90 * time.Minute, false},
		// Refusals.
		{"", 0, true},
		{"0", 0, true},
		{"-1", 0, true},
		{"0d", 0, true},
		{"0h", 0, true},
		{"-36h", 0, true},
		{"abc", 0, true},
		{"7x", 0, true},
		{"d", 0, true},
		// Out of range must be refused, never wrapped: 213504d used to
		// overflow to 25m26s and select almost every closed row.
		{"213504d", 0, true},
		{"213504", 0, true},
		{"30501w", 0, true},
		{"106752d", 0, true},
		{"9223372036854775807d", 0, true},
		{"99999999999h", 0, true},
		// A negative count far enough past the floor must be refused too,
		// never wrapped: -213503d used to overflow into a small positive
		// duration (~23h35m) and purge almost everything instead of being
		// rejected outright.
		{"-213503d", 0, true},
		{"-213503", 0, true},
		{"-9223372036854775808", 0, true},
		// The largest representable day count still parses.
		{"106751d", 106751 * day, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseOlderThan(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseOlderThan(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseOlderThan(%q) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}
}

// TestSweepConfirmHintCarriesEverySelector pins the no-force confirmation's
// suggested command against dropping a selector the caller gave: a hint that
// dropped --limit previewed one deletion and, followed literally, deleted
// every eligible row instead of the one the preview showed.
func TestSweepConfirmHintCarriesEverySelector(t *testing.T) {
	purge := purgeScope{cmdName: "purge", tier: issueops.SweepWispsPlane}
	prune := purgeScope{cmdName: "prune", tier: issueops.SweepDurable}
	tests := []struct {
		name      string
		scope     purgeScope
		olderThan string
		pattern   string
		limit     int
		want      string
	}{
		{"the gate's command", purge, "168h", "", 1, "bd purge --force --wisps-plane --older-than 168h --limit 1"},
		{"a larger limit", purge, "7d", "", 500, "bd purge --force --wisps-plane --older-than 7d --limit 500"},
		{"no limit asked", purge, "168h", "", 0, "bd purge --force --wisps-plane --older-than 168h"},
		{"pattern and limit", purge, "", "wi-*", 3, "bd purge --force --wisps-plane --pattern wi-* --limit 3"},
		{"prune defines no limit", prune, "30d", "", 0, "bd prune --force --older-than 30d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sweepConfirmHint(tt.scope, tt.olderThan, tt.pattern, tt.limit); got != tt.want {
				t.Errorf("sweepConfirmHint() = %q, want %q", got, tt.want)
			}
		})
	}
}
