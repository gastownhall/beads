package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/timeparsing"
)

// TestDeferUntilFormatHintCoversCompactUnits keeps the --until failure message
// and the parser it describes in step. "+3mo" is rejected while "+3m" is three
// months, so a message naming one example leaves the caller trying units one at
// a time to find the boundary. "min" is the sharpest case: "+3m" is months and
// "+30min" is minutes, which is why the parser's own doc comment spells out
// "m = months (NOT minutes; use \"min\" for minutes)".
func TestDeferUntilFormatHintCoversCompactUnits(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	for _, unit := range []string{"min", "h", "d", "w", "m", "y"} {
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
	for _, unit := range []string{"min=minutes", "h=hours", "d=days", "w=weeks", "m=months", "y=years"} {
		if !strings.Contains(output, unit) {
			t.Errorf("expected the error to name %q, got: %s", unit, output)
		}
	}
}

// TestRelativeTimeRejectionsShareTheHint guards the half of this fix that no
// behavioural test can reach cheaply. --defer and --due land on the same
// parser as --until, through the direct create and update paths and through
// their proxied-input twins, so a caller who mistypes any of them deserves the
// same unit set. A site that re-inlines its own example list still passes every
// call-level test while sending that caller back on the hunt, which is why the
// uniformity is pinned at the source. Blunt line matching, in the style of
// TestCobraParallelPolicyGuard.
//
// The matching is deliberately per-line, which constrains how a site may be
// written: a compliant call wrapped so that the format string and
// deferUntilFormatHint land on different lines would be reported here. All nine
// sites today are single-line, so keep them that way rather than loosening the
// guard.
func TestRelativeTimeRejectionsShareTheHint(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	rejection := regexp.MustCompile(`invalid --(?:until|defer|due) format`)
	found := 0

	for _, file := range sources {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !rejection.MatchString(line) {
				continue
			}
			found++
			if !strings.Contains(line, "deferUntilFormatHint") {
				t.Errorf("%s:%d rejects a relative-time flag without the shared hint: %s",
					file, i+1, strings.TrimSpace(line))
			}
		}
	}

	if found == 0 {
		t.Fatal("found no relative-time rejection sites; the guard is matching nothing")
	}
}

// TestGatherInputRejectionsNameUnitSet exercises the proxied-input paths for
// both flags that share the hint, so the unit set is proven to reach a real
// caller rather than only to be present in the constant.
func TestGatherInputRejectionsNameUnitSet(t *testing.T) {
	prevJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = prevJSON })

	units := []string{"min=minutes", "h=hours", "d=days", "w=weeks", "m=months", "y=years"}

	assertNamesUnits := func(t *testing.T, flag, output string) {
		t.Helper()
		if !strings.Contains(output, `invalid `+flag+` format "+3mo"`) {
			t.Fatalf("expected the rejected %s value in the error, got: %s", flag, output)
		}
		for _, unit := range units {
			if !strings.Contains(output, unit) {
				t.Errorf("expected the %s error to name %q, got: %s", flag, unit, output)
			}
		}
	}

	for _, flag := range []string{"--due", "--defer"} {
		t.Run("create "+flag, func(t *testing.T) {
			cmd := newCreateFlagsCommand(t, flag, "+3mo")
			var err error
			output := captureStderr(t, func() {
				_, err = gatherCreateInput(cmd, []string{"title"})
			})
			if err == nil {
				t.Fatalf("expected %s=+3mo to fail", flag)
			}
			assertNamesUnits(t, flag, output)
		})

		t.Run("update "+flag, func(t *testing.T) {
			cmd := &cobra.Command{Use: "update"}
			cmd.Flags().String("due", "", "Due date")
			cmd.Flags().String("defer", "", "Defer until")
			cmd.Flags().Bool("json", false, "JSON output")
			if err := cmd.ParseFlags([]string{flag, "+3mo"}); err != nil {
				t.Fatalf("parse update flags: %v", err)
			}
			var err error
			output := captureStderr(t, func() {
				_, err = gatherUpdateInput(t.Context(), cmd)
			})
			if err == nil {
				t.Fatalf("expected %s=+3mo to fail", flag)
			}
			assertNamesUnits(t, flag, output)
		})
	}

	// The four update sites use HandleErrorRespectJSON, which under --json emits
	// the message on STDOUT inside a JSON error payload instead of on stderr. The
	// subtests above pin jsonOutput = false, so that branch -- the one machine
	// consumers read -- would otherwise carry this ~190-char hint untested.
	// Assert on the DECODED message, not the raw bytes: json.Encoder HTML-escapes
	// the angle brackets in the hint's "[+-]?<n><unit>" notation, so a
	// strings.Contains over stdout would fail on a perfectly correct payload.
	t.Run("update --defer json", func(t *testing.T) {
		jsonOutput = true
		t.Cleanup(func() { jsonOutput = false })

		cmd := &cobra.Command{Use: "update"}
		cmd.Flags().String("due", "", "Due date")
		cmd.Flags().String("defer", "", "Defer until")
		cmd.Flags().Bool("json", false, "JSON output")
		if err := cmd.ParseFlags([]string{"--defer", "+3mo"}); err != nil {
			t.Fatalf("parse update flags: %v", err)
		}

		var err error
		output := captureStdout(t, func() error {
			_, err = gatherUpdateInput(t.Context(), cmd)
			return nil
		})
		if err == nil {
			t.Fatal("expected --defer=+3mo to fail")
		}

		var payload struct {
			Error string `json:"error"`
			Data  struct {
				Error string `json:"error"`
			} `json:"data"`
		}
		if jsonErr := json.Unmarshal([]byte(output), &payload); jsonErr != nil {
			t.Fatalf("stdout was not a JSON error payload (%v), got: %s", jsonErr, output)
		}
		message := payload.Error
		if message == "" {
			// BD_JSON_ENVELOPE=1 nests the same fields under "data".
			message = payload.Data.Error
		}
		if message == "" {
			t.Fatalf("no error message in the JSON payload, got: %s", output)
		}
		assertNamesUnits(t, "--defer", message)
	})
}
