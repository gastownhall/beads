//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedDoctorConventionsBlockedStatus pins conventions.blocked-status,
// the embedded-mode twin of bd doctor's Blocked Status advisory (bd-e5d8fd):
// bd doctor --check=conventions --json names an issue set to status 'blocked'
// with nothing blocking it, leaves the status as it is, and folds the warning
// into overall_ok the way every conventions check does (any warning clears
// it). A status-blocked issue an open blocker holds reports ok.
func TestEmbeddedDoctorConventionsBlockedStatus(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("held_by_hand_warns", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cbw")
		held := bdCreate(t, bd, dir, "Held by hand")
		bdUpdate(t, bd, dir, held.ID, "--status", "blocked")

		check, overallOK, allOK := conventionsBlockedStatus(t, bd, dir)
		if check.Status != statusWarning || !strings.HasPrefix(check.Detail, held.ID+". ") || !strings.Contains(check.Fix, "--status open") {
			t.Errorf("conventions.blocked-status = %+v, want a warning naming %s with the release guidance", check, held.ID)
		}
		if check.Category != "Conventions" {
			t.Errorf("conventions.blocked-status category = %q, want Conventions", check.Category)
		}
		if overallOK || overallOK != allOK {
			t.Errorf("overall_ok = %v with a conventions.blocked-status warning, want false", overallOK)
		}
		if got := bdShow(t, bd, dir, held.ID); got.Status != types.StatusBlocked {
			t.Errorf("%s status = %q after the check, want blocked", held.ID, got.Status)
		}
	})

	t.Run("held_by_a_blocker_is_ok", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cbo")
		blocker := bdCreate(t, bd, dir, "Open blocker")
		held := bdCreate(t, bd, dir, "Held by its blocker", "--deps", "depends-on:"+blocker.ID)
		bdUpdate(t, bd, dir, held.ID, "--status", "blocked")

		check, overallOK, allOK := conventionsBlockedStatus(t, bd, dir)
		if check.Status != statusOK {
			t.Errorf("conventions.blocked-status = %+v, want ok: %s is held by %s", check, held.ID, blocker.ID)
		}
		if overallOK != allOK {
			t.Errorf("overall_ok = %v, want %v (every conventions check ok)", overallOK, allOK)
		}
		if got := bdShow(t, bd, dir, held.ID); got.Status != types.StatusBlocked {
			t.Errorf("%s status = %q after the check, want blocked", held.ID, got.Status)
		}
	})
}

// conventionsBlockedStatus runs bd doctor --check=conventions --json in dir
// and returns its conventions.blocked-status check, its overall_ok, and
// whether every check it reported is ok.
func conventionsBlockedStatus(t *testing.T, bd, dir string) (check doctorCheck, overallOK, allOK bool) {
	t.Helper()
	stdout, stderr, err := runBDSplit(t, bd, dir, "doctor", "--check=conventions", "--json")
	if err != nil {
		t.Fatalf("bd doctor --check=conventions --json: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var result struct {
		Checks    []doctorCheck `json:"checks"`
		OverallOK bool          `json:"overall_ok"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("parse conventions JSON: %v\n%s", err, stdout)
	}
	i := slices.IndexFunc(result.Checks, func(c doctorCheck) bool { return c.Name == "conventions.blocked-status" })
	if i < 0 {
		t.Fatalf("bd doctor --check=conventions ran no conventions.blocked-status check:\n%s", stdout)
	}
	allOK = !slices.ContainsFunc(result.Checks, func(c doctorCheck) bool { return c.Status != statusOK })
	return result.Checks[i], result.OverallOK, allOK
}
