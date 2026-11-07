//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedDoctorConventionsBlockedStatus pins the embedded-lane shape of
// the GH#6565 advisory: bd doctor --check=conventions --json gains a
// conventions.blocked-status entry — ok on a clean database, a warning naming
// the held id on the drift row — a warning turns overall_ok false (the
// composite's any-warning rule), and the held status survives the check. The
// subtests share one workspace and run in order; only the second holds a row.
func TestEmbeddedDoctorConventionsBlockedStatus(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dcb")

	conventionsJSON := func(t *testing.T) (map[string]doctorCheck, bool) {
		t.Helper()
		stdout, stderr, err := runBDSplit(t, bd, dir, "doctor", "--check=conventions", "--json")
		if err != nil {
			t.Fatalf("bd doctor --check=conventions --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		var payload struct {
			Checks    []doctorCheck `json:"checks"`
			OverallOK bool          `json:"overall_ok"`
		}
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("parse conventions json: %v\n%s", err, stdout)
		}
		byName := map[string]doctorCheck{}
		for _, c := range payload.Checks {
			byName[c.Name] = c
		}
		if _, ok := byName["conventions.blocked-status"]; !ok {
			t.Fatalf("conventions ran no blocked-status check; checks: %+v", payload.Checks)
		}
		return byName, payload.OverallOK
	}

	t.Run("clean_database_reports_the_check_ok", func(t *testing.T) {
		byName, _ := conventionsJSON(t)
		if c := byName["conventions.blocked-status"]; c.Status != statusOK {
			t.Errorf("clean database: conventions.blocked-status = %q (%s / %s), want %q", c.Status, c.Message, c.Detail, statusOK)
		}
	})

	t.Run("held_row_warns_turns_overall_false_and_keeps_the_status", func(t *testing.T) {
		held := bdCreate(t, bd, dir, "Held past its blocker").ID
		if out, err := bdRunWithFlockRetry(t, bd, dir, "update", held, "--status", "blocked"); err != nil {
			t.Fatalf("bd update %s --status blocked failed: %v\n%s", held, err, out)
		}

		byName, overallOK := conventionsJSON(t)
		c := byName["conventions.blocked-status"]
		if c.Status != statusWarning {
			t.Fatalf("held row: conventions.blocked-status = %q (%s / %s), want %q", c.Status, c.Message, c.Detail, statusWarning)
		}
		if !strings.Contains(c.Detail, held) {
			t.Errorf("warning detail %q must name the held id %s", c.Detail, held)
		}
		if overallOK {
			t.Error("a conventions warning must turn overall_ok false (the composite's any-warning rule)")
		}
		if got := bdShow(t, bd, dir, held); got.Status != types.StatusBlocked {
			t.Errorf("status = %s after the check, want 'blocked' untouched", got.Status)
		}
	})
}
