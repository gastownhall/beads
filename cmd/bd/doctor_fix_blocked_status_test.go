package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

// blockedStatusFixGuidance is what bd doctor --fix prints for the Blocked
// Status advisory in place of a repair.
const blockedStatusFixGuidance = "  ⚠ Blocked Status is advisory and changes no status: review each issue, then release it with 'bd update <id>... --status open'\n"

// TestApplyFixListLeavesBlockedStatusAlone pins bd doctor --fix's dispatch of
// the Blocked Status advisory (bd-e5d8fd): it prints the manual-review
// guidance and moves on, opening no store (the path is an empty directory)
// and counting the check as neither fixed, skipped nor an error.
func TestApplyFixListLeavesBlockedStatusAlone(t *testing.T) {
	out := captureStdout(t, func() error {
		applyFixList(t.TempDir(), []doctorCheck{{
			Name:    doctor.BlockedStatusCheckName,
			Status:  statusWarning,
			Message: "1 issue(s) remain in status 'blocked' while their stored dependency-blocked flag is clear",
			Fix:     "Review each issue; if nothing holds it, release it with: bd update <id>... --status open.",
		}})
		return nil
	})
	if !strings.Contains(out, "\nFixing Blocked Status...\n"+blockedStatusFixGuidance) {
		t.Errorf("bd doctor --fix does not print the Blocked Status guidance:\n%s", out)
	}
	for _, unwanted := range []string{"Fixed", "No automatic fix available", "Skipped", "Error"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("bd doctor --fix prints %q for an advisory it does not repair:\n%s", unwanted, out)
		}
	}
	if !strings.Contains(out, "Fix summary: 0 fixed, 0 skipped, 0 errors\n") {
		t.Errorf("bd doctor --fix counts the Blocked Status advisory:\n%s", out)
	}
}
