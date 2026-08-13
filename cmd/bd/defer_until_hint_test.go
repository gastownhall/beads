package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/timeparsing"
)

// TestDeferUntilFormatHintCoversCompactUnits keeps the --until failure message
// and the parser it describes in step. "+3mo" is rejected while "+3m" is three
// months, so a message naming one example leaves the caller trying units one at
// a time to find the boundary.
func TestDeferUntilFormatHintCoversCompactUnits(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	for _, unit := range []string{"h", "d", "w", "m", "y"} {
		value := "+3" + unit
		if _, err := timeparsing.ParseRelativeTime(value, now); err != nil {
			t.Errorf("parser rejected %q, which the --until hint names as supported: %v", value, err)
		}
		if !strings.Contains(deferUntilFormatHint, unit+"=") {
			t.Errorf("--until hint does not name unit %q: %s", unit, deferUntilFormatHint)
		}
	}

	if _, err := timeparsing.ParseRelativeTime("+3mo", now); err == nil {
		t.Fatal("expected +3mo to be rejected; the hint exists to send that caller to +3m")
	}
	if !strings.Contains(deferUntilFormatHint, "+3m") {
		t.Errorf("--until hint should show the month form +3m: %s", deferUntilFormatHint)
	}
}

// TestDeferUntilRejectionNamesUnitSet exercises the real --until failure path
// and asserts the unit set reaches stderr, so dropping the hint from the error
// fails here rather than only degrading the message.
func TestDeferUntilRejectionNamesUnitSet(t *testing.T) {
	if err := deferCmd.Flags().Set("until", "+3mo"); err != nil {
		t.Fatalf("setting --until: %v", err)
	}
	t.Cleanup(func() {
		if err := deferCmd.Flags().Set("until", ""); err != nil {
			t.Fatalf("resetting --until: %v", err)
		}
		deferCmd.Flags().Lookup("until").Changed = false
	})

	var err error
	output := captureStderr(t, func() {
		err = deferCmd.RunE(deferCmd, []string{"t-1"})
	})

	if err == nil {
		t.Fatal("expected --until=+3mo to fail")
	}
	if !strings.Contains(output, `invalid --until format "+3mo"`) {
		t.Fatalf("expected the rejected value in the error, got: %s", output)
	}
	for _, unit := range []string{"h=hours", "d=days", "w=weeks", "m=months", "y=years"} {
		if !strings.Contains(output, unit) {
			t.Errorf("expected the error to name %q, got: %s", unit, output)
		}
	}
}
